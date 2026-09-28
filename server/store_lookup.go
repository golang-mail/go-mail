package server

import "github.com/golang-mail/go-mail/cache"

// Lookup helpers for the cache. Callers must hold store.Mu.

func findAccount(store *cache.Store, name string) *cache.Account {
	for _, acc := range store.Accounts {
		if acc.Name == name {
			return acc
		}
	}
	return nil
}

// findMailbox returns the account and mailbox; mailbox is nil if either is missing.
func findMailbox(store *cache.Store, account, mailbox string) (*cache.Account, *cache.Mailbox) {
	acc := findAccount(store, account)
	if acc == nil {
		return nil, nil
	}
	return acc, acc.Mailboxes[mailbox]
}

// findEmail returns account, mailbox and email; email is nil if any is missing.
func findEmail(store *cache.Store, account, mailbox string, uid uint32) (*cache.Account, *cache.Mailbox, *cache.CachedEmail) {
	acc, mbox := findMailbox(store, account, mailbox)
	if mbox == nil {
		return acc, nil, nil
	}
	return acc, mbox, mbox.Emails[uid]
}
