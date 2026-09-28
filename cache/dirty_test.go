package cache

import (
	"sort"
	"testing"
)

type recordingBackend struct {
	stubBackend
	savedEmails    []string
	savedMailboxes []string
	hintedEmails   []hintedSave
}

func (r *recordingBackend) SaveMailboxes(account string, _ map[string]*Mailbox) error {
	r.savedMailboxes = append(r.savedMailboxes, account)
	return nil
}

func (r *recordingBackend) SaveEmails(account, mailbox string, _ map[uint32]*CachedEmail) error {
	r.savedEmails = append(r.savedEmails, account+"/"+mailbox)
	return nil
}

func (r *recordingBackend) SaveEmailsWithHint(account, mailbox string, emails map[uint32]*CachedEmail, changedUIDs []uint32) error {
	r.hintedEmails = append(r.hintedEmails, hintedSave{account: account, mailbox: mailbox, uids: changedUIDs})
	return nil
}

type hintedSave struct {
	account, mailbox string
	uids             []uint32
}

func dirtyStore(rec *recordingBackend) *Store {
	s := NewStore()
	s.Backend = rec
	s.Accounts = []*Account{{Name: "acc", Mailboxes: map[string]*Mailbox{
		"INBOX":   {Name: "INBOX", Emails: map[uint32]*CachedEmail{1: {UID: 1}}},
		"Archive": {Name: "Archive", Emails: map[uint32]*CachedEmail{2: {UID: 2}}},
	}}}
	return s
}

func TestSaveCache_WritesOnlyDirtyMailboxes(t *testing.T) {
	rec := &recordingBackend{}
	s := dirtyStore(rec)

	s.SaveCache()
	if len(rec.savedEmails) != 0 {
		t.Fatalf("nothing dirty, yet emails saved: %v", rec.savedEmails)
	}
	if len(rec.savedMailboxes) != 1 {
		t.Fatalf("mailbox metadata should still be saved once, got %v", rec.savedMailboxes)
	}

	s.MarkDirty("acc", "Archive")
	s.SaveCache()
	if len(rec.savedEmails) != 1 || rec.savedEmails[0] != "acc/Archive" {
		t.Fatalf("want only acc/Archive saved, got %v", rec.savedEmails)
	}

	s.SaveCache()
	if len(rec.savedEmails) != 1 {
		t.Fatalf("dirty set must clear after a save, got %v", rec.savedEmails)
	}
}

func TestSaveCacheAll_WritesEverything(t *testing.T) {
	rec := &recordingBackend{}
	s := dirtyStore(rec)
	s.SaveCacheAll()
	sort.Strings(rec.savedEmails)
	if len(rec.savedEmails) != 2 || rec.savedEmails[0] != "acc/Archive" || rec.savedEmails[1] != "acc/INBOX" {
		t.Fatalf("full save must write every mailbox, got %v", rec.savedEmails)
	}
}

func TestMarkDirty_UnknownMailboxIsIgnoredOnSave(t *testing.T) {
	rec := &recordingBackend{}
	s := dirtyStore(rec)
	s.MarkDirty("acc", "Gone")
	s.SaveCache()
	if len(rec.savedEmails) != 0 {
		t.Fatalf("a dirty mark for a deleted mailbox must not write, got %v", rec.savedEmails)
	}
}

func TestMarkEmailDirty_SavesHintedOnly(t *testing.T) {
	rec := &recordingBackend{}
	s := dirtyStore(rec)

	s.MarkEmailDirty("acc", "INBOX", 1)
	s.SaveCache()
	if len(rec.savedEmails) != 0 {
		t.Fatalf("per-email mark must not trigger a full save, got %v", rec.savedEmails)
	}
	if len(rec.hintedEmails) != 1 || rec.hintedEmails[0].mailbox != "INBOX" {
		t.Fatalf("want one hinted save for acc/INBOX, got %v", rec.hintedEmails)
	}
	if len(rec.hintedEmails[0].uids) != 1 || rec.hintedEmails[0].uids[0] != 1 {
		t.Fatalf("hint must carry uid 1, got %v", rec.hintedEmails[0].uids)
	}

	s.SaveCache()
	if len(rec.hintedEmails) != 1 {
		t.Fatalf("hinted set must clear after a save, got %v", rec.hintedEmails)
	}
}

func TestMarkDirty_SupersedesEmailDirty(t *testing.T) {
	rec := &recordingBackend{}
	s := dirtyStore(rec)

	s.MarkEmailDirty("acc", "INBOX", 1)
	s.MarkDirty("acc", "INBOX")
	s.SaveCache()
	if len(rec.hintedEmails) != 0 {
		t.Fatalf("mailbox-dirty must supersede the uid hint, got %v", rec.hintedEmails)
	}
	if len(rec.savedEmails) != 1 || rec.savedEmails[0] != "acc/INBOX" {
		t.Fatalf("want one full save for acc/INBOX, got %v", rec.savedEmails)
	}
}

func TestMarkEmailDirty_IgnoredWhenMailboxFullyDirty(t *testing.T) {
	rec := &recordingBackend{}
	s := dirtyStore(rec)

	s.MarkDirty("acc", "INBOX")
	s.MarkEmailDirty("acc", "INBOX", 1)
	s.SaveCache()
	if len(rec.hintedEmails) != 0 {
		t.Fatalf("uid mark under a full-dirty mailbox must be skipped, got %v", rec.hintedEmails)
	}
}
