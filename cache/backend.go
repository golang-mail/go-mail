package cache

// CacheBackend defines the contract for persisting and loading the email cache.
type CacheBackend interface {
	// LoadMailboxes loads all mailbox metadata for the given account.
	LoadMailboxes(accountName string) (map[string]*Mailbox, error)

	// LoadEmails loads all cached emails for a given account and mailbox.
	LoadEmails(accountName, mailboxName string) (map[uint32]*CachedEmail, error)

	// SaveMailboxes persists the mailbox metadata for an account.
	SaveMailboxes(accountName string, mailboxes map[string]*Mailbox) error

	// SaveEmails persists all emails for a given account and mailbox.
	SaveEmails(accountName, mailboxName string, emails map[uint32]*CachedEmail) error

	// SaveEmailsWithHint persists only the listed emails, sparing the
	// untouched ones the change-detection cost. Backends that can't track
	// per-row changes just fall back to SaveEmails.
	SaveEmailsWithHint(accountName, mailboxName string, emails map[uint32]*CachedEmail, changedUIDs []uint32) error

	// ResetFiltered clears the Filtered flag on every stored email of the
	// account without rewriting anything else (used before a full filter run).
	ResetFiltered(accountName string) error

	// Close releases any resources held by the backend.
	Close() error
}
