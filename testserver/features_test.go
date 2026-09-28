package testserver

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

func appendMsg(t *testing.T, c *client.Client, mailbox, subject string, flags ...string) {
	t.Helper()
	raw := fmt.Sprintf("Subject: %s\r\nMessage-ID: <%s@x>\r\n\r\nbody of %s\r\n", subject, subject, subject)
	if err := c.Append(mailbox, flags, time.Now(), strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
}

func listNames(t *testing.T, c *client.Client, ref, pattern string, subscribedOnly bool) []string {
	t.Helper()
	ch := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() {
		if subscribedOnly {
			done <- c.Lsub(ref, pattern, ch)
		} else {
			done <- c.List(ref, pattern, ch)
		}
	}()
	var names []string
	for m := range ch {
		names = append(names, m.Name)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

func fetchFlags(t *testing.T, c *client.Client, uid uint32) []string {
	t.Helper()
	seq := new(imap.SeqSet)
	seq.AddNum(uid)
	ch := make(chan *imap.Message, 1)
	if err := c.UidFetch(seq, []imap.FetchItem{imap.FetchFlags}, ch); err != nil {
		t.Fatal(err)
	}
	m := <-ch
	if m == nil {
		t.Fatalf("uid %d not found", uid)
	}
	flags := append([]string(nil), m.Flags...)
	sort.Strings(flags)
	return flags
}

func TestUsers_ConfiguredAndAddedAtRuntime(t *testing.T) {
	ts := startWith(t, Config{SMTP: true, Users: []UserConfig{{Username: "second@x.test", Password: "s"}}})
	ts.AddUser("third", "t")

	for _, tc := range []struct{ user, pass string }{{"u", "p"}, {"second@x.test", "s"}, {"third", "t"}} {
		c, err := client.Dial(ts.Addr())
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Login(tc.user, tc.pass); err != nil {
			t.Fatalf("login %s: %v", tc.user, err)
		}
		if _, err := c.Select("INBOX", true); err != nil {
			t.Fatalf("%s has no INBOX: %v", tc.user, err)
		}
		c.Logout()
	}

	s := dialSMTP(t, ts.SMTPAddr())
	s.ehlo()
	s.say("MAIL FROM:<a@x>", "250")
	s.say("RCPT TO:<third@anywhere>", "250")
	s.say("RCPT TO:<second@x.test>", "250")
	s.say("RCPT TO:<fourth@x>", "550")
	s.say("DATA", "354")
	s.say("Subject: routed\r\n\r\nx\r\n.", "250")

	got := ts.Delivered()
	if len(got) != 1 {
		t.Fatalf("Delivered = %d messages, want 1", len(got))
	}
	d := got[0]
	sort.Strings(d.Users)
	if d.From != "a@x" || strings.Join(d.Recipients, ",") != "third@anywhere,second@x.test" || strings.Join(d.Users, ",") != "second@x.test,third" {
		t.Fatalf("delivered record = %+v", d)
	}
	if !strings.Contains(string(d.Raw), "Subject: routed") || d.Time.IsZero() {
		t.Fatalf("delivered raw/time = %q %v", d.Raw, d.Time)
	}
}

func TestStart_RejectsUserWithoutName(t *testing.T) {
	if _, err := Start(Config{Address: "127.0.0.1", Username: "u", Users: []UserConfig{{Password: "x"}}}); err == nil {
		t.Fatal("a users entry without a username must be refused")
	}
}

func TestSeed_LoadsOnceAndSkipsRestoredMailboxes(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "one.eml")
	f2 := filepath.Join(dir, "two.eml")
	os.WriteFile(f1, []byte("Subject: seed one\r\n\r\n1\r\n"), 0o644)
	os.WriteFile(f2, []byte("Subject: seed two\r\n\r\n2\r\n"), 0o644)
	store := filepath.Join(dir, "store")
	cfg := Config{StorageDir: store, Seed: []Seed{
		{File: f1},
		{Mailbox: "Archive", File: f2, Flags: []string{`\Seen`}},
	}}

	ts := startWith(t, cfg)
	c := login(t, ts.Addr())
	if st, err := c.Select("INBOX", true); err != nil || st.Messages != 1 {
		t.Fatalf("seeded INBOX = %+v, %v", st, err)
	}
	if st, err := c.Select("Archive", true); err != nil || st.Messages != 1 {
		t.Fatalf("seeded Archive = %+v, %v", st, err)
	}
	if flags := fetchFlags(t, c, 1); strings.Join(flags, ",") != `\Seen` {
		t.Fatalf("seed flags = %v", flags)
	}
	appendMsg(t, c, "INBOX", "arrived later")
	c.Logout()
	ts.Close()

	ts2 := startWith(t, cfg)
	c2 := login(t, ts2.Addr())
	if st, err := c2.Select("INBOX", true); err != nil || st.Messages != 2 {
		t.Fatalf("INBOX after restart = %+v, %v; seeds must not be re-applied", st, err)
	}
	if st, err := c2.Select("Archive", true); err != nil || st.Messages != 1 {
		t.Fatalf("Archive after restart = %+v, %v", st, err)
	}
}

func TestSeed_UnknownUserOrFileFails(t *testing.T) {
	if _, err := Start(Config{Address: "127.0.0.1", Username: "u", Seed: []Seed{{Username: "ghost", File: "/nonexistent"}}}); err == nil {
		t.Fatal("seed for an unknown user must fail")
	}
	if _, err := Start(Config{Address: "127.0.0.1", Username: "u", Seed: []Seed{{File: "/nonexistent.eml"}}}); err == nil {
		t.Fatal("seed with a missing file must fail")
	}
}

func TestFault_RejectAndCloseAfterCommands(t *testing.T) {
	ts := start(t)

	ts.FaultNextIMAPConnections(1, Fault{Reject: true})
	if _, err := client.Dial(ts.Addr()); err == nil {
		t.Fatal("rejected connection must fail to greet")
	}
	login(t, ts.Addr()) // the fault is consumed; the next connection works

	ts.FaultNextIMAPConnections(1, Fault{CloseAfterCommands: 2})
	c, err := client.Dial(ts.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login("u", "p"); err != nil {
		t.Fatalf("first command must still succeed: %v", err)
	}
	if err := c.Noop(); err == nil {
		if _, err := c.Select("INBOX", true); err == nil {
			t.Fatal("connection should have been dropped after two commands")
		}
	}
}

func TestFault_LatencyDelaysReplies(t *testing.T) {
	ts := start(t)
	ts.FaultNextIMAPConnections(1, Fault{Latency: 150 * time.Millisecond})
	c, err := client.Dial(ts.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	t0 := time.Now()
	if err := c.Login("u", "p"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d < 150*time.Millisecond {
		t.Fatalf("login answered in %v, want the injected latency", d)
	}
}

func TestList_PatternsAndSubscriptions(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	for _, m := range []string{"Work", "Work/2026", "Work/2026/Q1", "Personal"} {
		if err := c.Create(m); err != nil {
			t.Fatal(err)
		}
	}
	if got := listNames(t, c, "", "*", false); strings.Join(got, ",") != "INBOX,Personal,Work,Work/2026,Work/2026/Q1" {
		t.Fatalf(`LIST "" "*" = %v`, got)
	}
	if got := listNames(t, c, "", "%", false); strings.Join(got, ",") != "INBOX,Personal,Work" {
		t.Fatalf(`LIST "" "%%" = %v`, got)
	}
	if got := listNames(t, c, "Work/", "%", false); strings.Join(got, ",") != "Work/2026" {
		t.Fatalf(`LIST "Work/" "%%" = %v`, got)
	}
	if err := c.Unsubscribe("Personal"); err != nil {
		t.Fatal(err)
	}
	if got := listNames(t, c, "", "*", true); strings.Contains(strings.Join(got, ","), "Personal") {
		t.Fatalf("LSUB still lists an unsubscribed mailbox: %v", got)
	}
	if err := c.Subscribe("Personal"); err != nil {
		t.Fatal(err)
	}
	if got := listNames(t, c, "", "*", true); !strings.Contains(strings.Join(got, ","), "Personal") {
		t.Fatalf("LSUB misses a re-subscribed mailbox: %v", got)
	}
	if err := c.Subscribe("Nope"); err == nil {
		t.Fatal("subscribing an unknown mailbox must fail")
	}
}

func TestRename_MovesMailAndInboxSpecialCase(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	if err := c.Create("Old"); err != nil {
		t.Fatal(err)
	}
	appendMsg(t, c, "Old", "in old")
	if err := c.Rename("Old", "New"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("Old", true); err == nil {
		t.Fatal("renamed mailbox still selectable under the old name")
	}
	if st, err := c.Select("New", true); err != nil || st.Messages != 1 {
		t.Fatalf("New = %+v, %v", st, err)
	}
	if err := c.Rename("New", "INBOX"); err == nil {
		t.Fatal("renaming onto an existing mailbox must fail")
	}

	appendMsg(t, c, "INBOX", "in inbox")
	if err := c.Rename("INBOX", "Archived"); err != nil {
		t.Fatal(err)
	}
	if st, err := c.Select("Archived", true); err != nil || st.Messages != 1 {
		t.Fatalf("Archived = %+v, %v", st, err)
	}
	if st, err := c.Select("INBOX", true); err != nil || st.Messages != 0 {
		t.Fatalf("INBOX after rename = %+v, %v; must be empty but present", st, err)
	}
	ts.Flush()
	if _, err := os.Stat(filepath.Join(ts.st.dir, "u", "Old.yaml")); !os.IsNotExist(err) {
		t.Fatal("Old.yaml still on disk after rename")
	}
}

func TestStatus_ReportsCounts(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	appendMsg(t, c, "INBOX", "a", imap.SeenFlag)
	appendMsg(t, c, "INBOX", "b")
	appendMsg(t, c, "INBOX", "c", imap.DeletedFlag)
	st, err := c.Status("INBOX", []imap.StatusItem{imap.StatusMessages, imap.StatusUnseen, imap.StatusUidNext, imap.StatusUidValidity, imap.StatusRecent})
	if err != nil {
		t.Fatal(err)
	}
	if st.Messages != 3 || st.Unseen != 2 || st.UidNext != 4 || st.UidValidity == 0 || st.Recent != 0 {
		t.Fatalf("STATUS = %+v", st)
	}
	if _, err := c.Status("Missing", []imap.StatusItem{imap.StatusMessages}); err == nil {
		t.Fatal("STATUS on a missing mailbox must fail")
	}
}

func TestStore_SetAddRemoveAndSilent(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	appendMsg(t, c, "INBOX", "a", imap.SeenFlag)
	if _, err := c.Select("INBOX", false); err != nil {
		t.Fatal(err)
	}
	seq := new(imap.SeqSet)
	seq.AddNum(1)

	if err := c.UidStore(seq, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.FlaggedFlag, "custom"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := fetchFlags(t, c, 1); strings.Join(got, ",") != `\Flagged,\Seen,custom` {
		t.Fatalf("after +FLAGS = %v", got)
	}
	if err := c.UidStore(seq, imap.FormatFlagsOp(imap.RemoveFlags, true), []interface{}{imap.SeenFlag}, nil); err != nil {
		t.Fatal(err)
	}
	if got := fetchFlags(t, c, 1); strings.Join(got, ",") != `\Flagged,custom` {
		t.Fatalf("after -FLAGS = %v", got)
	}
	// A non-silent STORE echoes the new flags back.
	ch := make(chan *imap.Message, 1)
	if err := c.UidStore(seq, imap.FormatFlagsOp(imap.SetFlags, false), []interface{}{imap.AnsweredFlag}, ch); err != nil {
		t.Fatal(err)
	}
	m := <-ch
	if m == nil || strings.Join(m.Flags, ",") != `\Answered` {
		t.Fatalf("FLAGS echo = %+v", m)
	}
	if got := fetchFlags(t, c, 1); strings.Join(got, ",") != `\Answered` {
		t.Fatalf("after FLAGS = %v", got)
	}
}

func TestFetch_BodyMarksSeenButPeekDoesNot(t *testing.T) {
	ts := start(t)
	c := login(t, ts.Addr())
	appendMsg(t, c, "INBOX", "a")
	appendMsg(t, c, "INBOX", "b")
	if _, err := c.Select("INBOX", false); err != nil {
		t.Fatal(err)
	}
	// Counts the FETCH responses: a non-PEEK fetch also pushes the flag change.
	fetch := func(uid uint32, item imap.FetchItem) int {
		seq := new(imap.SeqSet)
		seq.AddNum(uid)
		ch := make(chan *imap.Message, 8)
		if err := c.UidFetch(seq, []imap.FetchItem{item}, ch); err != nil {
			t.Fatal(err)
		}
		return len(ch)
	}
	if n := fetch(1, "BODY.PEEK[]"); n != 1 {
		t.Fatalf("PEEK produced %d FETCH responses, want 1", n)
	}
	if got := fetchFlags(t, c, 1); len(got) != 0 {
		t.Fatalf("PEEK set flags %v", got)
	}
	if n := fetch(2, "BODY[]"); n != 2 {
		t.Fatalf("BODY[] produced %d FETCH responses, want the body plus the flag update", n)
	}
	if got := fetchFlags(t, c, 2); strings.Join(got, ",") != `\Seen` {
		t.Fatalf("BODY[] flags = %v, want \\Seen", got)
	}
}

func TestPersistence_ConcurrentWritesStayConsistent(t *testing.T) {
	dir := t.TempDir()
	ts := startWith(t, Config{StorageDir: dir})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := client.Dial(ts.Addr())
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Logout()
			if err := c.Login("u", "p"); err != nil {
				t.Error(err)
				return
			}
			mbox := fmt.Sprintf("Box%d", i)
			if err := c.Create(mbox); err != nil {
				t.Error(err)
				return
			}
			for j := range 10 {
				raw := fmt.Sprintf("Subject: %d-%d\r\n\r\nx\r\n", i, j)
				if err := c.Append(mbox, nil, time.Now(), strings.NewReader(raw)); err != nil {
					t.Error(err)
					return
				}
				if j%3 == 0 {
					ts.Flush()
				}
			}
		}()
	}
	wg.Wait()
	if err := ts.Close(); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(filepath.Join(dir, "u"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	ts2 := startWith(t, Config{StorageDir: dir})
	c := login(t, ts2.Addr())
	for i := range 8 {
		st, err := c.Select(fmt.Sprintf("Box%d", i), true)
		if err != nil || st.Messages != 10 || st.UidNext != 11 {
			t.Fatalf("Box%d after restart = %+v, %v", i, st, err)
		}
	}
}

func TestClose_IsIdempotentAndFreesPort(t *testing.T) {
	ts := startWith(t, Config{SMTP: true})
	addr := ts.Addr()
	if err := ts.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ts.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("port not released after Close: %v", err)
	}
	l.Close()
}
