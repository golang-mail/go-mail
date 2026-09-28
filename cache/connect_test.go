package cache

import (
	"strconv"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/testserver"
)

func tlsTestAccount(t *testing.T, mode string, insecure bool) *Account {
	t.Helper()
	ts, err := testserver.Start(testserver.Config{Address: "127.0.0.1", Username: "u", Password: "p", TLS: mode, StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	return &Account{Name: "acc", Server: "127.0.0.1", Port: strconv.Itoa(ts.Port()), Username: "u", Password: "p", TLS: mode, InsecureSkipVerify: insecure}
}

func TestConnect_TLSModes(t *testing.T) {
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode+" verifies by default", func(t *testing.T) {
			acc := tlsTestAccount(t, mode, false)
			c, err := acc.Connect()
			if err == nil {
				c.Logout()
				t.Fatal("a self-signed certificate must be rejected without insecure_skip_verify")
			}
			if !strings.Contains(strings.ToLower(err.Error()), "certificate") {
				t.Fatalf("error should be about the certificate, got %v", err)
			}
		})
		t.Run(mode+" with insecure_skip_verify", func(t *testing.T) {
			acc := tlsTestAccount(t, mode, true)
			c, err := acc.Connect()
			if err != nil {
				t.Fatal(err)
			}
			defer c.Logout()
			if _, err := c.Select("INBOX", true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConnect_WrongPasswordFails(t *testing.T) {
	acc := tlsTestAccount(t, "none", false)
	acc.Password = "nope"
	if c, err := acc.Connect(); err == nil {
		c.Logout()
		t.Fatal("wrong password must fail")
	}
}
