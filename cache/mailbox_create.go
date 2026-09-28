package cache

import (
	"strings"

	"github.com/emersion/go-imap/client"
)

// imapErrIs reports whether err's text contains needle, case-insensitively.
func imapErrIs(err error, needle string) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), needle)
}

// EnsureMailbox creates name, retrying under INBOX/ when the server rejects
// the namespace. Returns the name that now exists on the server.
func EnsureMailbox(c *client.Client, name string) (string, error) {
	err := c.Create(name)
	if err == nil || imapErrIs(err, "already exists") {
		return name, nil
	}
	if !imapErrIs(err, "nonexistent namespace") {
		return "", err
	}
	prefixed := "INBOX/" + name
	if err := c.Create(prefixed); err != nil && !imapErrIs(err, "already exists") {
		return "", err
	}
	return prefixed, nil
}
