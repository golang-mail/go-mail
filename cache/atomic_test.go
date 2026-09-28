package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteFileAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.yml")
	if err := WriteFileAtomic(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}

// Email rows of a removed mailbox would otherwise linger in the db forever.
func TestSQLiteSaveMailboxesPrunesOrphanEmails(t *testing.T) {
	b := NewSQLiteBackend(t.TempDir())
	defer b.Close()
	mailboxes := map[string]*Mailbox{
		"INBOX": {Name: "INBOX", Messages: 1},
		"Gone":  {Name: "Gone", Messages: 1, Unseen: 1},
	}
	if err := b.SaveMailboxes("acc", mailboxes); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveEmails("acc", "Gone", map[uint32]*CachedEmail{
		1: {UID: 1, Envelope: &Envelope{Subject: "orphan me", Date: time.Now()}, Filtered: true},
	}); err != nil {
		t.Fatal(err)
	}

	delete(mailboxes, "Gone")
	if err := b.SaveMailboxes("acc", mailboxes); err != nil {
		t.Fatal(err)
	}

	emails, err := b.LoadEmails("acc", "Gone")
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 0 {
		t.Fatalf("orphan rows survived mailbox deletion: %d", len(emails))
	}
}

// A hinted save must persist only the hinted rows while still deleting rows
// gone from the map, and keep the change-detection state consistent so a
// later full save stays a no-op for untouched rows.
func TestSQLiteSaveEmailsWithHint(t *testing.T) {
	b := NewSQLiteBackend(t.TempDir())
	defer b.Close()
	emails := map[uint32]*CachedEmail{
		1: {UID: 1, Envelope: &Envelope{Subject: "one", Date: time.Now()}, Body: "b1", Filtered: true},
		2: {UID: 2, Envelope: &Envelope{Subject: "two", Date: time.Now()}, Body: "b2", Filtered: true},
	}
	if err := b.SaveEmails("acc", "INBOX", emails); err != nil {
		t.Fatal(err)
	}

	// Label toggle on uid 1: hint only that row.
	emails[1].Labels = []string{"work"}
	if err := b.SaveEmailsWithHint("acc", "INBOX", emails, []uint32{1}); err != nil {
		t.Fatal(err)
	}

	// Deletion must still be detected without hashing: drop uid 2, hint uid 1.
	delete(emails, 2)
	emails[1].Labels = append(emails[1].Labels, "urgent")
	if err := b.SaveEmailsWithHint("acc", "INBOX", emails, []uint32{1}); err != nil {
		t.Fatal(err)
	}

	loaded, err := b.LoadEmails("acc", "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[1] == nil {
		t.Fatalf("want only uid 1 after hinted save + delete, got %v", loaded)
	}
	if len(loaded[1].Labels) != 2 || loaded[1].Labels[0] != "work" || loaded[1].Labels[1] != "urgent" {
		t.Fatalf("hinted row not persisted: labels = %v", loaded[1].Labels)
	}

	// Consistency: a full save right after must find nothing new to write
	// (idempotent), and must not resurrect the deleted row.
	if err := b.SaveEmails("acc", "INBOX", emails); err != nil {
		t.Fatal(err)
	}
	loaded, err = b.LoadEmails("acc", "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[2] != nil {
		t.Fatalf("full save after hint must keep state, got %v", loaded)
	}
}
