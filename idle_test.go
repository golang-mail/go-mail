package main

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/client"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/testserver"
)

func TestIdleEnabled(t *testing.T) {
	f, tr := false, true
	tests := []struct {
		name string
		acc  *cache.Account
		want bool
	}{
		{"default on", &cache.Account{}, true},
		{"explicit on", &cache.Account{Idle: &tr}, true},
		{"opted out", &cache.Account{Idle: &f}, false},
		{"send-only never idles", &cache.Account{SendOnly: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := idleEnabled(tt.acc); got != tt.want {
				t.Fatalf("idleEnabled = %v want %v", got, tt.want)
			}
		})
	}
}

func TestDebounceCoalescesBursts(t *testing.T) {
	in := make(chan struct{}, 16)
	var fired atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go debounce(ctx, in, 50*time.Millisecond, func() { fired.Add(1) })

	for range 5 {
		in <- struct{}{}
	}
	time.Sleep(200 * time.Millisecond)
	if got := fired.Load(); got != 1 {
		t.Fatalf("burst of 5 fired %d times, want 1", got)
	}
	in <- struct{}{}
	time.Sleep(200 * time.Millisecond)
	if got := fired.Load(); got != 2 {
		t.Fatalf("second burst: fired %d times, want 2", got)
	}
}

func idleTestAccount(t *testing.T) *cache.Account {
	ts, err := testserver.Start(testserver.Config{Address: "127.0.0.1", Username: "u", Password: "p", TLS: "none", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	return &cache.Account{Name: "acc", Server: "127.0.0.1", Port: strconv.Itoa(ts.Port()), Username: "u", Password: "p", TLS: "none"}
}

func TestIdleLoop_FiresOnNewMail(t *testing.T) {
	acc := idleTestAccount(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changed := make(chan string, 8)
	go runIdleLoop(ctx, acc, "INBOX", 50*time.Millisecond, func(mailbox string) { changed <- mailbox })

	// Wait until the loop is idling before appending.
	deadline := time.Now().Add(5 * time.Second)
	for idleConnections(acc.Name) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if idleConnections(acc.Name) == 0 {
		t.Fatal("idle loop never connected")
	}

	c, err := client.Dial(net.JoinHostPort(acc.Server, acc.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	if err := c.Login("u", "p"); err != nil {
		t.Fatal(err)
	}
	if err := c.Append("INBOX", nil, time.Now(), strings.NewReader("Subject: new\r\n\r\nx\r\n")); err != nil {
		t.Fatal(err)
	}

	select {
	case mb := <-changed:
		if mb != "INBOX" {
			t.Fatalf("changed mailbox %q", mb)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle loop did not report the new mail")
	}

	cancel()
	deadline = time.Now().Add(3 * time.Second)
	for idleConnections(acc.Name) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if idleConnections(acc.Name) != 0 {
		t.Fatal("idle loop kept its connection after cancel")
	}
}
