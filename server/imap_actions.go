package server

import (
	"fmt"
	"log/slog"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"github.com/golang-mail/go-mail/cache"
)

// withIMAP connects to acc and runs fn, logging any failure under what.
// Meant to run as a goroutine after the cache has already been updated.
func withIMAP(acc *cache.Account, what string, fn func(c *client.Client) error) {
	c, err := acc.Acquire()
	if err != nil {
		slog.Error("failed to connect for "+what, "account", acc.Name, "error", err)
		return
	}
	defer acc.Release(c)
	if err := fn(c); err != nil {
		slog.Error("failed to "+what, "account", acc.Name, "error", err)
	}
}

// withMailbox is withIMAP with mailbox selected read-write first.
func withMailbox(acc *cache.Account, mailbox, what string, fn func(c *client.Client) error) {
	withIMAP(acc, what, func(c *client.Client) error {
		if _, err := c.Select(mailbox, false); err != nil {
			return fmt.Errorf("select %s: %w", mailbox, err)
		}
		return fn(c)
	})
}

// withMailboxSync is withMailbox that reports the failure, for handlers
// whose response depends on the IMAP result.
func withMailboxSync(acc *cache.Account, mailbox, what string, fn func(c *client.Client) error) error {
	c, err := acc.Acquire()
	if err != nil {
		return fmt.Errorf("connect for %s: %w", what, err)
	}
	defer acc.Release(c)
	if _, err := c.Select(mailbox, false); err != nil {
		return fmt.Errorf("select %s: %w", mailbox, err)
	}
	return fn(c)
}

func uidSeq(uids ...uint32) *imap.SeqSet {
	s := new(imap.SeqSet)
	for _, uid := range uids {
		s.AddNum(uid)
	}
	return s
}

// storeFlags adds or removes flags on uids; a no-op for an empty set.
func storeFlags(c *client.Client, uids []uint32, op imap.FlagsOp, flags ...string) error {
	if len(uids) == 0 {
		return nil
	}
	items := make([]interface{}, len(flags))
	for i, f := range flags {
		items[i] = f
	}
	return c.UidStore(uidSeq(uids...), imap.FormatFlagsOp(op, true), items, nil)
}

func moveUIDs(c *client.Client, uids []uint32, to string) error {
	if len(uids) == 0 {
		return nil
	}
	return c.UidMove(uidSeq(uids...), to)
}

// deleteUIDs flags uids \Deleted and expunges the selected mailbox.
func deleteUIDs(c *client.Client, uids []uint32) error {
	if len(uids) == 0 {
		return nil
	}
	if err := storeFlags(c, uids, imap.AddFlags, imap.DeletedFlag); err != nil {
		return err
	}
	return c.Expunge(nil)
}
