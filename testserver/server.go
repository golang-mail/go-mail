// Package testserver runs an in-process IMAP and SMTP server for tests, the
// e2e suite and the embedded `test:` block in server.yml.
package testserver

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// Config describes one test server. Port 0 picks a free port; SMTPPort 0
// with SMTP set to true does the same, SMTPPort 0 without it disables SMTP.
type Config struct {
	Address  string
	Port     int
	SMTPPort int
	// SMTP forces the SMTP listener on even when SMTPPort is 0.
	SMTP     bool
	Username string
	Password string
	// Users adds accounts beyond Username; SMTP routes to them by address.
	Users []UserConfig
	// TLS is "none" (default), "starttls" or "tls". Both TLS modes use a
	// self-signed certificate exposed through TestServer.CertPool.
	TLS        string
	StorageDir string
	// Seed files mail into mailboxes that have no persisted state yet.
	Seed []Seed
}

// UserConfig is one extra account.
type UserConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Seed is one raw RFC 5322 message file to load at first start. Username
// defaults to Config.Username, Mailbox to INBOX.
type Seed struct {
	Username string   `yaml:"username"`
	Mailbox  string   `yaml:"mailbox"`
	File     string   `yaml:"file"`
	Flags    []string `yaml:"flags"`
}

// TestServer holds both the IMAP and SMTP test servers.
type TestServer struct {
	imap     *imapserver.Server
	imapLn   net.Listener
	faults   *faultListener
	smtp     *smtpServer
	st       *store
	imapAddr string
	smtpAddr string
	certPEM  []byte

	closeOnce sync.Once
	closeErr  error
}

// Addr is the IMAP listener address, host:port.
func (ts *TestServer) Addr() string { return ts.imapAddr }

// Port is the IMAP listener port.
func (ts *TestServer) Port() int { return portOf(ts.imapAddr) }

// SMTPAddr is the SMTP listener address, empty when SMTP is disabled.
func (ts *TestServer) SMTPAddr() string { return ts.smtpAddr }

// SMTPPort is the SMTP listener port, 0 when SMTP is disabled.
func (ts *TestServer) SMTPPort() int { return portOf(ts.smtpAddr) }

func portOf(addr string) int {
	if addr == "" {
		return 0
	}
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	var n int
	fmt.Sscanf(p, "%d", &n)
	return n
}

// CertPEM is the self-signed certificate in TLS modes, nil otherwise.
func (ts *TestServer) CertPEM() []byte { return ts.certPEM }

// CertPool trusts only the server's own certificate; nil in plain mode.
func (ts *TestServer) CertPool() *x509.CertPool {
	if ts.certPEM == nil {
		return nil
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ts.certPEM)
	return pool
}

// Deliver appends a raw message to a user's mailbox as if it had arrived,
// waking idlers. The mailbox is created when missing.
func (ts *TestServer) Deliver(username, mailbox string, raw []byte, flags ...string) (uint32, error) {
	u := ts.st.user(username)
	if u == nil {
		return 0, fmt.Errorf("testserver: no user %q", username)
	}
	if mailbox == "" {
		mailbox = "INBOX"
	}
	u.mutex.Lock()
	mbox := u.ensureMailboxLocked(mailbox)
	u.mutex.Unlock()

	imapFlags := make([]imap.Flag, 0, len(flags))
	for _, f := range flags {
		imapFlags = append(imapFlags, imap.Flag(f))
	}
	data := mbox.appendBytes(raw, &imap.AppendOptions{Flags: imapFlags, Time: time.Now()})
	return uint32(data.UID), nil
}

// Flush persists pending state now. Tests call it before reading StorageDir.
func (ts *TestServer) Flush() error { return ts.st.flush() }

// AddUser registers another account at runtime; an existing name is replaced.
func (ts *TestServer) AddUser(username, password string) {
	u := ts.st.addUser(username, password)
	u.mutex.Lock()
	u.password = password
	u.ensureMailboxLocked("INBOX")
	u.mutex.Unlock()
}

// FaultNextIMAPConnections applies f to the next count IMAP connections.
func (ts *TestServer) FaultNextIMAPConnections(count int, f Fault) {
	ts.faults.enqueue(count, f)
}

// Delivered lists every message the SMTP server accepted, oldest first.
func (ts *TestServer) Delivered() []DeliveredMessage {
	if ts.smtp == nil {
		return nil
	}
	return ts.smtp.delivered()
}

// Close stops both listeners and writes the final state.
func (ts *TestServer) Close() error {
	ts.closeOnce.Do(func() {
		var errs []error
		if ts.smtp != nil {
			errs = append(errs, ts.smtp.Close())
		}
		if ts.imap != nil {
			errs = append(errs, ts.imap.Close())
			// Serve may not have registered the listener yet.
			if err := ts.imapLn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
		errs = append(errs, ts.st.close())
		ts.closeErr = errors.Join(errs...)
	})
	return ts.closeErr
}

type tlsMode int

const (
	tlsNone tlsMode = iota
	tlsStart
	tlsImplicit
)

func parseTLSMode(s string) (tlsMode, error) {
	switch strings.ToLower(s) {
	case "", "none":
		return tlsNone, nil
	case "starttls":
		return tlsStart, nil
	case "tls":
		return tlsImplicit, nil
	}
	return tlsNone, fmt.Errorf("testserver: unknown tls mode %q (want none, starttls or tls)", s)
}

func Start(cfg Config) (*TestServer, error) {
	mode, err := parseTLSMode(cfg.TLS)
	if err != nil {
		return nil, err
	}
	if cfg.Username == "" {
		return nil, errors.New("testserver: username is required")
	}

	// Configured users exist before load so their fixtures are picked up
	// even without a users.yaml.
	st := newStore(cfg.StorageDir)
	st.addUser(cfg.Username, cfg.Password)
	for _, uc := range cfg.Users {
		if uc.Username == "" {
			return nil, errors.New("testserver: users entries need a username")
		}
		st.addUser(uc.Username, uc.Password)
	}
	loaded, err := st.load()
	if err != nil {
		return nil, fmt.Errorf("loading test server state: %w", err)
	}
	if err := seedMailboxes(st, cfg, loaded); err != nil {
		return nil, err
	}
	for _, user := range st.userList() {
		user.mutex.Lock()
		user.ensureMailboxLocked("INBOX")
		user.mutex.Unlock()
	}

	ts := &TestServer{st: st}

	var tlsConfig *tls.Config
	if mode != tlsNone {
		cert, certPEM, err := selfSignedCert(cfg.Address)
		if err != nil {
			return nil, fmt.Errorf("testserver: certificate: %w", err)
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		ts.certPEM = certPEM
	}

	rawLn, err := net.Listen("tcp", net.JoinHostPort(cfg.Address, fmt.Sprint(cfg.Port)))
	if err != nil {
		return nil, fmt.Errorf("imap listen: %w", err)
	}
	// Faults act on the raw bytes, underneath TLS.
	ts.faults = &faultListener{Listener: rawLn}
	var imapLn net.Listener = ts.faults
	if mode == tlsImplicit {
		imapLn = tls.NewListener(imapLn, tlsConfig)
	}
	ts.imapAddr = imapLn.Addr().String()

	opts := &imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &session{st: st}, nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1:    {},
			imap.CapNamespace:    {},
			imap.CapUIDPlus:      {},
			imap.CapESearch:      {},
			imap.CapListExtended: {},
			imap.CapListStatus:   {},
			imap.CapMove:         {},
			imap.CapStatusSize:   {},
		},
		Logger:       slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
		InsecureAuth: mode == tlsNone,
	}
	if mode == tlsStart {
		// TLSConfig turns on STARTTLS; implicit TLS is handled by the listener.
		opts.TLSConfig = tlsConfig
	}
	srv := imapserver.New(opts)
	go func() {
		if err := srv.Serve(imapLn); err != nil && !strings.Contains(err.Error(), "server closed") {
			slog.Error("testserver IMAP serve error", "error", err)
		}
	}()
	ts.imap = srv
	ts.imapLn = imapLn

	if cfg.SMTPPort > 0 || cfg.SMTP {
		smtpLn, err := net.Listen("tcp", net.JoinHostPort(cfg.Address, fmt.Sprint(cfg.SMTPPort)))
		if err != nil {
			srv.Close()
			return nil, fmt.Errorf("smtp listen: %w", err)
		}
		if mode == tlsImplicit {
			smtpLn = tls.NewListener(smtpLn, tlsConfig)
		}
		var startTLS *tls.Config
		if mode == tlsStart {
			startTLS = tlsConfig
		}
		ts.smtp = startSMTP(st, smtpLn, startTLS)
		ts.smtpAddr = smtpLn.Addr().String()
		slog.Info("test SMTP server listening", "address", ts.smtpAddr)
	}

	return ts, nil
}

// seedMailboxes appends each Seed file to its mailbox unless that mailbox
// was restored from disk, so restarts with a StorageDir do not duplicate.
func seedMailboxes(st *store, cfg Config, loaded map[string]map[string]bool) error {
	for _, sd := range cfg.Seed {
		username := sd.Username
		if username == "" {
			username = cfg.Username
		}
		mailbox := normalizeName(sd.Mailbox)
		if mailbox == "" {
			mailbox = "INBOX"
		}
		if loaded[username][mailbox] {
			continue
		}
		u := st.user(username)
		if u == nil {
			return fmt.Errorf("testserver: seed for unknown user %q", username)
		}
		raw, err := os.ReadFile(sd.File)
		if err != nil {
			return fmt.Errorf("testserver: seed: %w", err)
		}
		flags := make([]imap.Flag, 0, len(sd.Flags))
		for _, f := range sd.Flags {
			flags = append(flags, imap.Flag(f))
		}
		u.mutex.Lock()
		mbox := u.ensureMailboxLocked(mailbox)
		u.mutex.Unlock()
		mbox.appendBytes(raw, &imap.AppendOptions{Flags: flags, Time: time.Now()})
	}
	return nil
}
