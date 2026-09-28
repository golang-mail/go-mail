package testserver

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// idleOn puts a fresh client into IDLE on a mailbox and returns its update channel.
func idleOn(t *testing.T, addr, mailbox string) (<-chan client.Update, func()) {
	t.Helper()
	c := login(t, addr)
	if _, err := c.Select(mailbox, false); err != nil {
		t.Fatal(err)
	}
	updates := make(chan client.Update, 16)
	c.Updates = updates
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- c.Idle(stop, &client.IdleOptions{PollInterval: -1}) }()
	return updates, func() {
		close(stop)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("IDLE did not stop")
		}
	}
}

func waitMailboxUpdate(t *testing.T, updates <-chan client.Update, what string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case u := <-updates:
			if _, ok := u.(*client.MailboxUpdate); ok {
				return
			}
		case <-deadline:
			t.Fatalf("no mailbox update pushed after %s", what)
		}
	}
}

func TestIDLE_PushesUpdateOnAppend(t *testing.T) {
	ts := start(t)
	updates, stop := idleOn(t, ts.Addr(), "INBOX")
	defer stop()

	other := login(t, ts.Addr())
	msg := "Subject: pushed\r\nMessage-ID: <push@x>\r\n\r\nhi\r\n"
	if err := other.Append("INBOX", []string{imap.SeenFlag}, time.Now(), strings.NewReader(msg)); err != nil {
		t.Fatal(err)
	}
	waitMailboxUpdate(t, updates, "APPEND")
}

func TestIDLE_PushesUpdateOnSMTPDelivery(t *testing.T) {
	ts := start(t)
	updates, stop := idleOn(t, ts.Addr(), "INBOX")
	defer stop()

	s := dialSMTP(t, ts.SMTPAddr())
	s.ehlo()
	s.say("MAIL FROM:<a@x>", "250")
	s.say("RCPT TO:<u@local>", "250")
	s.say("DATA", "354")
	s.say("Subject: via smtp\r\n\r\nbody\r\n.", "250")
	waitMailboxUpdate(t, updates, "SMTP delivery")
}

func TestIDLE_PushesExpunge(t *testing.T) {
	ts := start(t)
	if _, err := ts.Deliver("u", "INBOX", []byte("Subject: doomed\r\n\r\nx\r\n"), `\Deleted`); err != nil {
		t.Fatal(err)
	}
	updates, stop := idleOn(t, ts.Addr(), "INBOX")
	defer stop()

	other := login(t, ts.Addr())
	if _, err := other.Select("INBOX", false); err != nil {
		t.Fatal(err)
	}
	if err := other.Expunge(nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case u := <-updates:
			if _, ok := u.(*client.ExpungeUpdate); ok {
				return
			}
		case <-deadline:
			t.Fatal("no EXPUNGE pushed to the idling session")
		}
	}
}
