package cache

import (
	"strconv"
	"testing"

	"github.com/emersion/go-imap/client"

	"github.com/golang-mail/go-mail/testserver"
)

func poolTestAccount(t *testing.T) *Account {
	ts, err := testserver.Start(testserver.Config{Address: "127.0.0.1", Username: "u", Password: "p", TLS: "none", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	return &Account{Name: "acc", Server: "127.0.0.1", Port: strconv.Itoa(ts.Port()), Username: "u", Password: "p", TLS: "none"}
}

func TestPool_ReusesReleasedConnection(t *testing.T) {
	acc := poolTestAccount(t)
	c1, err := acc.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	acc.Release(c1)
	c2, err := acc.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Release(c2)
	if c1 != c2 {
		t.Fatal("a released healthy connection must be handed out again")
	}
	if _, err := c2.Select("INBOX", true); err != nil {
		t.Fatalf("reused connection unusable: %v", err)
	}
}

func TestPool_DropsLoggedOutConnection(t *testing.T) {
	acc := poolTestAccount(t)
	c1, err := acc.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	c1.Logout()
	acc.Release(c1)
	c2, err := acc.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Release(c2)
	if c1 == c2 {
		t.Fatal("a logged-out connection must not be reused")
	}
	if _, err := c2.Select("INBOX", true); err != nil {
		t.Fatalf("fresh connection unusable: %v", err)
	}
}

func TestPool_CapsIdleConnections(t *testing.T) {
	acc := poolTestAccount(t)
	var conns []*client.Client
	for range 4 {
		c, err := acc.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		acc.Release(c)
	}
	if n := acc.idleConnections(); n != poolMaxIdle {
		t.Fatalf("pool holds %d idle connections, want %d", n, poolMaxIdle)
	}
}
