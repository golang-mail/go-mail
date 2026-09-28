package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"time"

	"github.com/emersion/go-imap"

	"github.com/golang-mail/go-mail/cache"
)

// appendCopy files msg in mailbox over IMAP and mirrors it into the cache so
// it shows up before the next sync. The mailbox is created (with the INBOX/
// fallback) when it cannot be selected. Returns the new UID (0 when the server
// could not report one) and the mailbox name actually used.
func appendCopy(store *cache.Store, acc *cache.Account, mailbox string, flags []string, msg []byte, env *cache.Envelope, body, contentType string) (uint32, string, error) {
	c, err := acc.Acquire()
	if err != nil {
		return 0, mailbox, fmt.Errorf("connect: %w", err)
	}
	defer acc.Release(c)

	target := mailbox
	if _, err := c.Select(target, false); err != nil {
		created, err := cache.EnsureMailbox(c, target)
		if err != nil {
			return 0, target, fmt.Errorf("create %s: %w", target, err)
		}
		target = created
	}
	date := env.Date
	if date.IsZero() {
		date = time.Now()
	}
	if err := c.Append(target, flags, date, bytes.NewReader(msg)); err != nil {
		return 0, target, fmt.Errorf("append to %s: %w", target, err)
	}
	if _, err := c.Select(target, false); err != nil {
		return 0, target, fmt.Errorf("reselect %s: %w", target, err)
	}
	criteria := imap.NewSearchCriteria()
	criteria.Header = map[string][]string{"Message-ID": {env.MessageID}}
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return 0, target, fmt.Errorf("search appended message: %w", err)
	}
	if len(uids) == 0 {
		return 0, target, nil
	}
	uid := uids[len(uids)-1]

	store.Mu.Lock()
	defer store.Mu.Unlock()
	if acc.Mailboxes == nil {
		acc.Mailboxes = map[string]*cache.Mailbox{}
	}
	mbox := acc.Mailboxes[target]
	if mbox == nil {
		mbox = &cache.Mailbox{Name: target}
		acc.Mailboxes[target] = mbox
	}
	if mbox.Emails == nil {
		mbox.Emails = map[uint32]*cache.CachedEmail{}
	}
	mbox.Emails[uid] = &cache.CachedEmail{
		UID:         uid,
		Envelope:    env,
		Body:        body,
		ContentType: contentType,
		Flags:       flags,
		// Visible immediately rather than held until the next filter run.
		Filtered: true,
	}
	mbox.Messages++
	store.MarkDirty(acc.Name, target)
	return uid, target, nil
}

// appendCopyAsync files the copy in the background so the HTTP response does
// not wait on IMAP; failures are logged under what.
func appendCopyAsync(store *cache.Store, acc *cache.Account, mailbox string, flags []string, msg []byte, env *cache.Envelope, body, contentType, what string) {
	go func() {
		if _, _, err := appendCopy(store, acc, mailbox, flags, msg, env, body, contentType); err != nil {
			slog.Error("failed to "+what, "account", acc.Name, "mailbox", mailbox, "error", err)
		}
	}()
}
