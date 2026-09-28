package server

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/emersion/go-imap/client"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/testserver"
)

// startIMAP runs the in-process test server and returns an account pointed at it.
func startIMAP(t *testing.T) *cache.Account {
	ts, err := testserver.Start(testserver.Config{
		Address: "127.0.0.1", Username: "u", Password: "p", TLS: "none", StorageDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	return &cache.Account{Name: "acc", Server: "127.0.0.1", Port: strconv.Itoa(ts.Port()), Username: "u", Password: "p", TLS: "none",
		Mailboxes: map[string]*cache.Mailbox{"INBOX": {Name: "INBOX", Emails: map[uint32]*cache.CachedEmail{}}}}
}

func TestAppendCopy_AppendsToIMAPAndCaches(t *testing.T) {
	acc := startIMAP(t)
	store := cache.NewStore()
	store.Accounts = []*cache.Account{acc}
	store.Mu.Lock()
	acc.Mailboxes["Sent"] = &cache.Mailbox{Name: "Sent", Emails: map[uint32]*cache.CachedEmail{}}
	store.Mu.Unlock()

	msg := []byte("From: me@x\r\nTo: you@y\r\nSubject: copy\r\nMessage-ID: <abc@go-mail>\r\n\r\nbody\r\n")
	env := &cache.Envelope{Date: time.Now(), Subject: "copy", MessageID: "<abc@go-mail>"}
	uid, _, err := appendCopy(store, acc, "Sent", []string{`\Seen`}, msg, env, "body", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if uid == 0 {
		t.Fatal("want a UID for the appended message")
	}

	store.Mu.RLock()
	got := acc.Mailboxes["Sent"].Emails[uid]
	store.Mu.RUnlock()
	if got == nil || got.Env().Subject != "copy" || !got.Filtered || got.Body != "body" {
		t.Fatalf("cached copy = %+v", got)
	}

	c, err := client.Dial(net.JoinHostPort(acc.Server, acc.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	if err := c.Login("u", "p"); err != nil {
		t.Fatal(err)
	}
	status, err := c.Select("Sent", true)
	if err != nil {
		t.Fatal(err)
	}
	if status.Messages != 1 {
		t.Fatalf("IMAP Sent has %d messages, want 1", status.Messages)
	}
}

func TestAppendCopy_CreatesMissingMailbox(t *testing.T) {
	acc := startIMAP(t)
	store := cache.NewStore()
	store.Accounts = []*cache.Account{acc}
	msg := []byte("Subject: x\r\nMessage-ID: <m@go-mail>\r\n\r\nx\r\n")
	if _, _, err := appendCopy(store, acc, "Brand-New", nil, msg, &cache.Envelope{MessageID: "<m@go-mail>", Date: time.Now()}, "x", "text/plain"); err != nil {
		t.Fatal(err)
	}
}

func TestAppendCopyAsync_EventuallyCaches(t *testing.T) {
	acc := startIMAP(t)
	store := cache.NewStore()
	store.Accounts = []*cache.Account{acc}
	env := &cache.Envelope{Date: time.Now(), Subject: "later", MessageID: "<async@go-mail>"}
	msg := []byte("Subject: later\r\nMessage-ID: <async@go-mail>\r\n\r\nx\r\n")

	appendCopyAsync(store, acc, "Sent", []string{`\Seen`}, msg, env, "x", "text/plain", "save test copy")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		store.Mu.RLock()
		_, mbox := findMailbox(store, "acc", "Sent")
		n := 0
		if mbox != nil {
			n = len(mbox.Emails)
		}
		store.Mu.RUnlock()
		if n == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("copy never showed up in the cache")
}
