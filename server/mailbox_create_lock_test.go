package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Mailbox creation talks to IMAP; the store lock must be released while it
// waits on the network so readers and other handlers are not stalled.
func TestMailboxCreate_DoesNotHoldStoreLockAcrossIMAP(t *testing.T) {
	// A listener that accepts and never sends a greeting keeps Connect blocked.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	store := newHandlerStore(nil)
	store.Accounts[0].Server = host
	store.Accounts[0].Port = port
	store.Accounts[0].TLS = "none"

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		w := httptest.NewRecorder()
		mailboxHandler(store)(w, httptest.NewRequest(http.MethodPost, "/api/mailbox?account=acc&mailbox=New", nil))
	}()

	var conn net.Conn
	select {
	case conn = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never dialed IMAP")
	}
	defer conn.Close()

	// The handler is now blocked in Connect. A read lock must still be available.
	locked := make(chan struct{})
	go func() {
		store.Mu.RLock()
		_ = len(store.Accounts)
		store.Mu.RUnlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("store.Mu held across IMAP connect")
	}

	conn.Close()
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish after IMAP connection closed")
	}
}
