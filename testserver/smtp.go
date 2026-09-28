package testserver

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
)

// DeliveredMessage is one message the SMTP server accepted.
type DeliveredMessage struct {
	From       string
	Recipients []string
	// Users are the accounts whose INBOX received a copy.
	Users []string
	Raw   []byte
	Time  time.Time
}

// smtpServer accepts mail and files it into the recipients' INBOX.
type smtpServer struct {
	st       *store
	listener net.Listener
	startTLS *tls.Config

	wg    sync.WaitGroup
	mu    sync.Mutex
	conns map[net.Conn]struct{}
	inbox []DeliveredMessage
}

func (s *smtpServer) delivered() []DeliveredMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DeliveredMessage, len(s.inbox))
	copy(out, s.inbox)
	return out
}

func startSMTP(st *store, l net.Listener, startTLS *tls.Config) *smtpServer {
	s := &smtpServer{st: st, listener: l, startTLS: startTLS, conns: make(map[net.Conn]struct{})}
	s.wg.Add(1)
	go s.serve()
	return s
}

// Close stops accepting, drops open connections and waits for handlers.
func (s *smtpServer) Close() error {
	err := s.listener.Close()
	s.mu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *smtpServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				slog.Error("testserver SMTP accept error", "error", err)
			}
			return
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Go(func() {
			s.handleConn(conn)
			s.mu.Lock()
			delete(s.conns, conn)
			s.mu.Unlock()
		})
	}
}

type smtpSession struct {
	srv  *smtpServer
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer

	from      string
	rcpts     []*User
	rcptAddrs []string
}

func (ss *smtpSession) reply(line string) {
	fmt.Fprintf(ss.w, "%s\r\n", line)
	ss.w.Flush()
}

func (ss *smtpSession) readLine() (string, error) {
	line, err := ss.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (ss *smtpSession) reset() {
	ss.from = ""
	ss.rcpts = nil
	ss.rcptAddrs = nil
}

func (ss *smtpSession) isTLS() bool {
	_, ok := ss.conn.(*tls.Conn)
	return ok
}

func (s *smtpServer) handleConn(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Minute))

	ss := &smtpSession{srv: s, conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn)}
	ss.reply("220 localhost Go-Mail Test SMTP Server")

	for {
		line, err := ss.readLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)
		arg = strings.TrimSpace(arg)

		switch verb {
		case "EHLO", "HELO":
			ss.reset()
			ss.reply("250-localhost")
			if s.startTLS != nil && !ss.isTLS() {
				ss.reply("250-STARTTLS")
			}
			ss.reply("250-AUTH PLAIN LOGIN")
			ss.reply("250 8BITMIME")

		case "STARTTLS":
			if s.startTLS == nil || ss.isTLS() {
				ss.reply("454 TLS not available")
				continue
			}
			ss.reply("220 Ready to start TLS")
			tlsConn := tls.Server(conn, s.startTLS)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			ss.conn = tlsConn
			ss.r = bufio.NewReader(tlsConn)
			ss.w = bufio.NewWriter(tlsConn)
			ss.reset()

		case "AUTH":
			ss.handleAuth(arg)

		case "MAIL":
			if !strings.HasPrefix(strings.ToUpper(arg), "FROM:") {
				ss.reply("501 Syntax: MAIL FROM:<address>")
				continue
			}
			ss.from = extractAddr(arg[5:])
			ss.rcpts = nil
			ss.rcptAddrs = nil
			ss.reply("250 OK")

		case "RCPT":
			if !strings.HasPrefix(strings.ToUpper(arg), "TO:") {
				ss.reply("501 Syntax: RCPT TO:<address>")
				continue
			}
			addr := extractAddr(arg[3:])
			u := s.st.routeRecipient(addr)
			if u == nil {
				ss.reply("550 No such user here")
				continue
			}
			ss.rcpts = append(ss.rcpts, u)
			ss.rcptAddrs = append(ss.rcptAddrs, addr)
			ss.reply("250 OK")

		case "DATA":
			if len(ss.rcpts) == 0 {
				ss.reply("503 RCPT TO first")
				continue
			}
			ss.reply("354 Start mail input; end with <CRLF>.<CRLF>")
			data, err := ss.readData()
			if err != nil {
				return
			}
			s.deliver(ss.from, ss.rcptAddrs, ss.rcpts, data)
			ss.reset()
			ss.reply("250 OK message delivered")

		case "RSET":
			ss.reset()
			ss.reply("250 OK")

		case "NOOP":
			ss.reply("250 OK")

		case "QUIT":
			ss.reply("221 Bye")
			return

		default:
			ss.reply("502 Command not recognized")
		}
	}
}

// readData consumes the message body up to the lone dot, undoing dot-stuffing.
func (ss *smtpSession) readData() ([]byte, error) {
	var data []byte
	for {
		line, err := ss.readLine()
		if err != nil {
			return nil, err
		}
		if line == "." {
			return data, nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		data = append(data, line...)
		data = append(data, '\r', '\n')
	}
}

// handleAuth checks PLAIN and LOGIN credentials against the configured users.
func (ss *smtpSession) handleAuth(arg string) {
	mech, initial, _ := strings.Cut(arg, " ")
	challenge := func(prompt string) (string, bool) {
		ss.reply("334 " + prompt)
		line, err := ss.readLine()
		if err != nil || line == "*" {
			ss.reply("501 Authentication cancelled")
			return "", false
		}
		return line, true
	}

	var username, password string
	switch strings.ToUpper(mech) {
	case "PLAIN":
		resp := initial
		if resp == "" {
			var ok bool
			if resp, ok = challenge(""); !ok {
				return
			}
		}
		raw, err := base64.StdEncoding.DecodeString(resp)
		parts := strings.Split(string(raw), "\x00")
		if err != nil || len(parts) != 3 {
			ss.reply("501 Malformed PLAIN response")
			return
		}
		username, password = parts[1], parts[2]
	case "LOGIN":
		userB64 := initial
		if userB64 == "" {
			var ok bool
			if userB64, ok = challenge(base64.StdEncoding.EncodeToString([]byte("Username:"))); !ok {
				return
			}
		}
		passB64, ok := challenge(base64.StdEncoding.EncodeToString([]byte("Password:")))
		if !ok {
			return
		}
		u, err1 := base64.StdEncoding.DecodeString(userB64)
		p, err2 := base64.StdEncoding.DecodeString(passB64)
		if err1 != nil || err2 != nil {
			ss.reply("501 Malformed LOGIN response")
			return
		}
		username, password = string(u), string(p)
	default:
		ss.reply("504 Unrecognized authentication type")
		return
	}

	u := ss.srv.st.user(username)
	if u == nil || u.login(username, password) != nil {
		ss.reply("535 Authentication credentials invalid")
		return
	}
	ss.reply("235 Authentication successful")
}

// extractAddr pulls the address out of "<user@host>" or a bare "user@host",
// dropping any ESMTP parameters that follow.
func extractAddr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "<"); i >= 0 {
		if j := strings.Index(s, ">"); j > i {
			return s[i+1 : j]
		}
	}
	if i := strings.IndexByte(s, ' '); i >= 0 {
		s = s[:i]
	}
	return s
}

// deliver files one copy per distinct recipient user and records the message.
func (s *smtpServer) deliver(from string, rcptAddrs []string, rcpts []*User, data []byte) {
	dm := DeliveredMessage{From: from, Recipients: rcptAddrs, Raw: data, Time: time.Now()}
	seen := make(map[*User]bool, len(rcpts))
	for _, u := range rcpts {
		if seen[u] {
			continue
		}
		seen[u] = true
		dm.Users = append(dm.Users, u.username)
		u.mutex.Lock()
		mbox := u.ensureMailboxLocked("INBOX")
		u.mutex.Unlock()
		mbox.appendBytes(data, &imap.AppendOptions{Time: dm.Time})
		slog.Info("testserver SMTP delivered message", "from", from, "user", u.username, "bytes", len(data))
	}
	s.mu.Lock()
	s.inbox = append(s.inbox, dm)
	s.mu.Unlock()
}
