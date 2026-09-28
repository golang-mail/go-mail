package testserver

import (
	"os"
	"path/filepath"
	"time"
)

// WriteMailbox writes a mailbox fixture where Start will load it. Zero Size,
// Uid and UidNext fields are filled in; the mailbox is subscribed.
func WriteMailbox(storageDir, username string, mb StoredMailbox) error {
	if mb.Name == "" {
		mb.Name = "INBOX"
	}
	mb.Name = normalizeName(mb.Name)
	mb.Subscribed = true
	if mb.UidValidity == 0 {
		mb.UidValidity = 1
	}
	var maxUID uint32
	for _, m := range mb.Messages {
		if m.Uid == 0 {
			m.Uid = maxUID + 1
		}
		if m.Uid > maxUID {
			maxUID = m.Uid
		}
		if m.Size == 0 {
			m.Size = uint32(len(m.Body))
		}
		if m.Date.IsZero() {
			m.Date = time.Now()
		}
		if m.Flags == nil {
			m.Flags = []string{}
		}
	}
	if mb.UidNext <= maxUID {
		mb.UidNext = maxUID + 1
	}

	dir := filepath.Join(storageDir, username)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeYAMLAtomic(filepath.Join(dir, mailboxFileName(mb.Name)), &mb)
}
