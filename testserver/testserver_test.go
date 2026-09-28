package testserver

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

func startWith(t *testing.T, cfg Config) *TestServer {
	t.Helper()
	if cfg.Address == "" {
		cfg.Address = "127.0.0.1"
	}
	if cfg.Username == "" {
		cfg.Username, cfg.Password = "u", "p"
	}
	if cfg.StorageDir == "" {
		cfg.StorageDir = t.TempDir()
	}
	ts, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	return ts
}

func start(t *testing.T) *TestServer {
	t.Helper()
	return startWith(t, Config{SMTP: true, TLS: "none"})
}

func login(t *testing.T, addr string) *client.Client {
	t.Helper()
	c, err := client.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Logout() })
	if err := c.Login("u", "p"); err != nil {
		t.Fatal(err)
	}
	return c
}

// smtpTalk drives a raw SMTP dialogue, failing on an unexpected reply prefix.
type smtpTalk struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dialSMTP(t *testing.T, addr string) *smtpTalk {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	st := &smtpTalk{t: t, conn: conn, r: bufio.NewReader(conn)}
	st.expect("220")
	return st
}

func (s *smtpTalk) send(line string) {
	fmt.Fprintf(s.conn, "%s\r\n", line)
}

func (s *smtpTalk) expect(prefix string) string {
	s.t.Helper()
	line, err := s.r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, prefix) {
		s.t.Fatalf("got %q (%v), want prefix %s", line, err, prefix)
	}
	return line
}

func (s *smtpTalk) say(line, wantPrefix string) string {
	s.t.Helper()
	s.send(line)
	return s.expect(wantPrefix)
}

// ehlo reads the multi-line greeting and returns the advertised extensions.
func (s *smtpTalk) ehlo() []string {
	s.t.Helper()
	s.send("EHLO test")
	var exts []string
	for {
		line := s.expect("250")
		exts = append(exts, strings.TrimSpace(line[4:]))
		if line[3] == ' ' {
			return exts
		}
	}
}

func plainAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pass))
}

func TestStart_PicksFreePorts(t *testing.T) {
	ts := start(t)
	if ts.Port() == 0 || ts.SMTPPort() == 0 {
		t.Fatalf("ports = %d/%d, want both assigned", ts.Port(), ts.SMTPPort())
	}
	if ts.Addr() != fmt.Sprintf("127.0.0.1:%d", ts.Port()) {
		t.Fatalf("Addr %q does not match Port %d", ts.Addr(), ts.Port())
	}
	login(t, ts.Addr())
}

func TestStart_NoSMTPWithoutPort(t *testing.T) {
	ts := startWith(t, Config{})
	if ts.SMTPAddr() != "" || ts.SMTPPort() != 0 {
		t.Fatalf("SMTP listener started without being asked: %q", ts.SMTPAddr())
	}
}

func TestStart_RejectsUnknownTLSMode(t *testing.T) {
	_, err := Start(Config{Address: "127.0.0.1", Username: "u", TLS: "ssl"})
	if err == nil {
		t.Fatal("unknown tls mode must be refused")
	}
}

func TestIMAP_RejectsBadPassword(t *testing.T) {
	ts := start(t)
	c, err := client.Dial(ts.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	if err := c.Login("u", "wrong"); err == nil {
		t.Fatal("bad password must be rejected")
	}
}

func TestIMAP_CreateAppendSelect(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	if err := c.Create("Archive"); err != nil {
		t.Fatal(err)
	}
	msg := "From: a@b\r\nSubject: hello\r\nMessage-ID: <1@x>\r\n\r\nbody\r\n"
	if err := c.Append("Archive", []string{imap.SeenFlag}, time.Now(), strings.NewReader(msg)); err != nil {
		t.Fatal(err)
	}
	status, err := c.Select("Archive", true)
	if err != nil {
		t.Fatal(err)
	}
	if status.Messages != 1 {
		t.Fatalf("Archive has %d messages, want 1", status.Messages)
	}
	criteria := imap.NewSearchCriteria()
	criteria.Header = map[string][]string{"Message-ID": {"<1@x>"}}
	uids, err := c.UidSearch(criteria)
	if err != nil || len(uids) != 1 {
		t.Fatalf("UID search by Message-ID = %v, %v", uids, err)
	}
	if err := c.Delete("INBOX"); err == nil {
		t.Fatal("deleting INBOX must fail")
	}
}

func TestIMAP_InboxIsCaseInsensitive(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	if err := c.Append("inbox", nil, time.Now(), strings.NewReader("Subject: x\r\n\r\ny\r\n")); err != nil {
		t.Fatalf("append to lowercase inbox: %v", err)
	}
	status, err := c.Select("Inbox", true)
	if err != nil {
		t.Fatalf("select mixed-case Inbox: %v", err)
	}
	if status.Messages != 1 {
		t.Fatalf("Inbox has %d messages, want 1", status.Messages)
	}
	if err := c.Create("INbox"); err == nil {
		t.Fatal("creating INBOX under another spelling must fail")
	}
}

func TestIMAP_CopyToSameMailboxIsRefused(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	if err := c.Append("INBOX", nil, time.Now(), strings.NewReader("Subject: x\r\n\r\ny\r\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("INBOX", false); err != nil {
		t.Fatal(err)
	}
	seq := new(imap.SeqSet)
	seq.AddNum(1)
	done := make(chan error, 1)
	go func() { done <- c.UidCopy(seq, "INBOX") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("copy onto itself must be refused")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("copy onto itself deadlocked")
	}
}

func TestIMAP_MoveWakesIdlerOnDestination(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	if err := c.Create("Archive"); err != nil {
		t.Fatal(err)
	}
	if err := c.Append("INBOX", nil, time.Now(), strings.NewReader("Subject: x\r\n\r\ny\r\n")); err != nil {
		t.Fatal(err)
	}

	updates, stop := idleOn(t, ts.Addr(), "Archive")
	defer stop()

	if _, err := c.Select("INBOX", false); err != nil {
		t.Fatal(err)
	}
	seq := new(imap.SeqSet)
	seq.AddNum(1)
	if err := c.UidMove(seq, "Archive"); err != nil {
		t.Fatal(err)
	}
	waitMailboxUpdate(t, updates, "MOVE into the idled mailbox")

	status, err := c.Select("Archive", true)
	if err != nil {
		t.Fatal(err)
	}
	if status.Messages != 1 {
		t.Fatalf("Archive has %d messages after move, want 1", status.Messages)
	}
}

func TestIMAP_SearchSurvivesMalformedMessage(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	// No header/body separator and an invalid header line.
	if err := c.Append("INBOX", nil, time.Now(), strings.NewReader("\x00\xff garbage without headers")); err != nil {
		t.Fatal(err)
	}
	if err := c.Append("INBOX", nil, time.Now(), strings.NewReader("Message-ID: <ok@x>\r\n\r\nfine\r\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("INBOX", true); err != nil {
		t.Fatal(err)
	}
	criteria := imap.NewSearchCriteria()
	criteria.Header = map[string][]string{"Message-ID": {"<ok@x>"}}
	uids, err := c.UidSearch(criteria)
	if err != nil || len(uids) != 1 || uids[0] != 2 {
		t.Fatalf("search across a malformed message = %v, %v; want [2]", uids, err)
	}
	criteria = imap.NewSearchCriteria()
	criteria.Text = []string{"fine"}
	if uids, err = c.UidSearch(criteria); err != nil || len(uids) != 1 {
		t.Fatalf("TEXT search across a malformed message = %v, %v", uids, err)
	}
}

func TestIMAP_UIDValidityChangesWhenRecreated(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	if err := c.Create("Tmp"); err != nil {
		t.Fatal(err)
	}
	first, err := c.Select("Tmp", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Delete("Tmp"); err != nil {
		t.Fatal(err)
	}
	if err := c.Create("Tmp"); err != nil {
		t.Fatal(err)
	}
	second, err := c.Select("Tmp", true)
	if err != nil {
		t.Fatal(err)
	}
	if first.UidValidity == second.UidValidity {
		t.Fatalf("UIDVALIDITY %d reused after delete and re-create", first.UidValidity)
	}
}

func TestDeliver_AppendsAndWakesIdler(t *testing.T) {
	ts := start(t)
	updates, stop := idleOn(t, ts.Addr(), "INBOX")
	defer stop()

	uid, err := ts.Deliver("u", "", []byte("Subject: direct\r\n\r\nhi\r\n"), `\Seen`)
	if err != nil {
		t.Fatal(err)
	}
	if uid != 1 {
		t.Fatalf("first delivered UID = %d, want 1", uid)
	}
	waitMailboxUpdate(t, updates, "Deliver")

	if _, err := ts.Deliver("nobody", "", []byte("x")); err == nil {
		t.Fatal("Deliver to an unknown user must fail")
	}
	if _, err := ts.Deliver("u", "Auto/Created", []byte("Subject: a\r\n\r\nb\r\n")); err != nil {
		t.Fatal(err)
	}
	c := login(t, ts.Addr())
	status, err := c.Select("Auto/Created", true)
	if err != nil || status.Messages != 1 {
		t.Fatalf("Deliver must create the mailbox: %v, %+v", err, status)
	}
}

func TestSMTP_DeliversToInbox(t *testing.T) {
	ts := start(t)
	s := dialSMTP(t, ts.SMTPAddr())
	s.ehlo()
	s.say("MAIL FROM:<sender@x>", "250")
	s.say("RCPT TO:<u@local>", "250")
	s.say("DATA", "354")
	s.say("Subject: via smtp\r\n\r\n..dot-stuffed\r\n.", "250")
	s.say("QUIT", "221")

	c := login(t, ts.Addr())
	status, err := c.Select("INBOX", true)
	if err != nil {
		t.Fatal(err)
	}
	if status.Messages != 1 {
		t.Fatalf("INBOX has %d messages after SMTP delivery, want 1", status.Messages)
	}
	criteria := imap.NewSearchCriteria()
	criteria.Body = []string{".dot-stuffed"}
	if uids, err := c.UidSearch(criteria); err != nil || len(uids) != 1 {
		t.Fatalf("dot-stuffing was not undone: %v, %v", uids, err)
	}
}

func TestSMTP_ChecksCredentials(t *testing.T) {
	ts := start(t)
	s := dialSMTP(t, ts.SMTPAddr())
	exts := s.ehlo()
	if !contains(exts, "AUTH PLAIN LOGIN") {
		t.Fatalf("EHLO did not advertise AUTH: %v", exts)
	}
	s.say("AUTH PLAIN "+plainAuth("u", "wrong"), "535")
	s.say("AUTH PLAIN "+plainAuth("u", "p"), "235")

	s.say("AUTH LOGIN", "334")
	s.say(base64.StdEncoding.EncodeToString([]byte("u")), "334")
	s.say(base64.StdEncoding.EncodeToString([]byte("nope")), "535")
	s.say("AUTH LOGIN", "334")
	s.say(base64.StdEncoding.EncodeToString([]byte("u")), "334")
	s.say(base64.StdEncoding.EncodeToString([]byte("p")), "235")
}

func TestSMTP_RoutesByRecipient(t *testing.T) {
	dir := t.TempDir()
	users := "- username: u\n  password: p\n- username: other@example.test\n  password: q\n"
	if err := os.WriteFile(filepath.Join(dir, "users.yaml"), []byte(users), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := startWith(t, Config{SMTP: true, StorageDir: dir})

	s := dialSMTP(t, ts.SMTPAddr())
	s.ehlo()
	s.say("MAIL FROM:<sender@x>", "250")
	s.say("RCPT TO:<nobody@x>", "550")
	s.say("RCPT TO:<OTHER@example.test>", "250")
	s.say("RCPT TO:<other@example.test>", "250")
	s.say("DATA", "354")
	s.say("Subject: routed\r\n\r\nx\r\n.", "250")

	for _, tc := range []struct {
		user, pass string
		want       uint32
	}{{"u", "p", 0}, {"other@example.test", "q", 1}} {
		c, err := client.Dial(ts.Addr())
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Login(tc.user, tc.pass); err != nil {
			t.Fatal(err)
		}
		status, err := c.Select("INBOX", true)
		if err != nil {
			t.Fatal(err)
		}
		if status.Messages != tc.want {
			t.Fatalf("%s INBOX has %d messages, want %d (duplicate recipients must file one copy)", tc.user, status.Messages, tc.want)
		}
		c.Logout()
	}
}

func TestSMTP_RejectsDataWithoutRecipients(t *testing.T) {
	ts := start(t)
	s := dialSMTP(t, ts.SMTPAddr())
	s.ehlo()
	s.say("MAIL FROM:<sender@x>", "250")
	s.say("DATA", "503")
}

func TestPersistence_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ts := startWith(t, Config{StorageDir: dir})
	c := login(t, ts.Addr())
	if err := c.Create("Keep/Me"); err != nil {
		t.Fatal(err)
	}
	if err := c.Create("Gone"); err != nil {
		t.Fatal(err)
	}
	if err := c.Append("Keep/Me", []string{imap.FlaggedFlag}, time.Now(), strings.NewReader("Subject: kept\r\n\r\nx\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete("Gone"); err != nil {
		t.Fatal(err)
	}
	if err := c.Append("INBOX", nil, time.Now(), strings.NewReader("Subject: two\r\n\r\nx\r\n")); err != nil {
		t.Fatal(err)
	}
	c.Logout()
	if err := ts.Close(); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(filepath.Join(dir, "u"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if contains(names, "Gone.yaml") || !contains(names, "Keep_Me.yaml") || !contains(names, "INBOX.yaml") {
		t.Fatalf("files after close = %v", names)
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".tmp") {
			t.Fatalf("temp file left behind: %s", n)
		}
	}

	ts2 := startWith(t, Config{StorageDir: dir})
	c2 := login(t, ts2.Addr())
	status, err := c2.Select("Keep/Me", true)
	if err != nil {
		t.Fatal(err)
	}
	if status.Messages != 1 || status.UidNext != 2 {
		t.Fatalf("restored Keep/Me = %+v", status)
	}
	if _, err := c2.Select("Gone", true); err == nil {
		t.Fatal("deleted mailbox came back after restart")
	}
	if _, err := c2.Select("Keep/Me", true); err != nil {
		t.Fatal(err)
	}
	seq := new(imap.SeqSet)
	seq.AddNum(1)
	ch := make(chan *imap.Message, 1)
	if err := c2.Fetch(seq, []imap.FetchItem{imap.FetchFlags}, ch); err != nil {
		t.Fatal(err)
	}
	if m := <-ch; !contains(m.Flags, imap.FlaggedFlag) {
		t.Fatalf("flags lost across restart: %v", m.Flags)
	}
}

func TestWriteMailbox_SeedsFixture(t *testing.T) {
	dir := t.TempDir()
	err := WriteMailbox(dir, "u", StoredMailbox{Messages: []*StoredMessage{
		{Body: []byte("Subject: one\r\n\r\n1\r\n")},
		{Body: []byte("Subject: two\r\n\r\n2\r\n"), Flags: []string{`\Seen`}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ts := startWith(t, Config{StorageDir: dir})
	c := login(t, ts.Addr())
	status, err := c.Select("INBOX", true)
	if err != nil {
		t.Fatal(err)
	}
	if status.Messages != 2 || status.UidNext != 3 || status.UnseenSeqNum != 1 {
		t.Fatalf("seeded INBOX = %+v", status)
	}
	st, err := c.Status("INBOX", []imap.StatusItem{imap.StatusUnseen})
	if err != nil || st.Unseen != 1 {
		t.Fatalf("seeded INBOX unseen = %v, %v; want 1", st, err)
	}
}

func TestTLS_ImplicitTLS(t *testing.T) {
	ts := startWith(t, Config{SMTP: true, TLS: "tls"})
	tlsCfg := &tls.Config{RootCAs: ts.CertPool(), ServerName: "127.0.0.1"}

	c, err := client.DialTLS(ts.Addr(), tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	if err := c.Login("u", "p"); err != nil {
		t.Fatal(err)
	}

	conn, err := tls.Dial("tcp", ts.SMTPAddr(), tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sc, err := smtp.NewClient(conn, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Auth(smtp.PlainAuth("", "u", "p", "127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	sc.Quit()
}

func TestTLS_StartTLS(t *testing.T) {
	ts := startWith(t, Config{SMTP: true, TLS: "starttls"})
	tlsCfg := &tls.Config{RootCAs: ts.CertPool(), ServerName: "127.0.0.1"}

	c, err := client.Dial(ts.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	if err := c.Login("u", "p"); err == nil {
		t.Fatal("login before STARTTLS must be refused")
	}
	if err := c.StartTLS(tlsCfg); err != nil {
		t.Fatal(err)
	}
	if err := c.Login("u", "p"); err != nil {
		t.Fatalf("login after STARTTLS: %v", err)
	}

	sc, err := smtp.Dial(ts.SMTPAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if ok, _ := sc.Extension("STARTTLS"); !ok {
		t.Fatal("SMTP EHLO must advertise STARTTLS")
	}
	if err := sc.StartTLS(tlsCfg); err != nil {
		t.Fatal(err)
	}
	if ok, _ := sc.Extension("STARTTLS"); ok {
		t.Fatal("STARTTLS still advertised after the upgrade")
	}
	if err := sc.Auth(smtp.PlainAuth("", "u", "p", "127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	if err := sc.Mail("me@x"); err != nil {
		t.Fatal(err)
	}
	if err := sc.Rcpt("u@x"); err != nil {
		t.Fatal(err)
	}
	w, err := sc.Data()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(w, "Subject: over tls\r\n\r\nhello\r\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	sc.Quit()

	status, err := c.Select("INBOX", true)
	if err != nil || status.Messages != 1 {
		t.Fatalf("INBOX after STARTTLS delivery = %+v, %v", status, err)
	}
}

func contains(list []string, want string) bool {
	return slices.Contains(list, want)
}
