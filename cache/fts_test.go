package cache

import (
	"testing"
	"time"
)

func ftsEmails() map[uint32]*CachedEmail {
	return map[uint32]*CachedEmail{
		1: {UID: 1, Envelope: &Envelope{Subject: "Quarterly planning", Date: time.Now(), From: []*Address{{PersonalName: "Olive", MailboxName: "olive", HostName: "example.com"}}}, Body: "agenda attached", Filtered: true},
		2: {UID: 2, Envelope: &Envelope{Subject: "Lunch", Date: time.Now(), From: []*Address{{MailboxName: "bob", HostName: "corp.example"}}}, Body: "pizza on friday", Filtered: true},
	}
}

func TestSQLite_SearchText(t *testing.T) {
	b := NewSQLiteBackend(t.TempDir())
	defer b.Close()
	if err := b.SaveEmails("acc", "INBOX", ftsEmails()); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveEmails("acc", "Archive", map[uint32]*CachedEmail{7: {UID: 7, Envelope: &Envelope{Subject: "old quarterly notes"}, Filtered: true}}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		mailbox string
		words   []string
		want    map[string][]uint32
	}{
		{"subject word", "", []string{"quarterly"}, map[string][]uint32{"INBOX": {1}, "Archive": {7}}},
		{"prefix", "", []string{"quart"}, map[string][]uint32{"INBOX": {1}, "Archive": {7}}},
		{"body word", "", []string{"pizza"}, map[string][]uint32{"INBOX": {2}}},
		{"address", "", []string{"olive"}, map[string][]uint32{"INBOX": {1}}},
		{"all words must match", "", []string{"quarterly", "pizza"}, map[string][]uint32{}},
		{"mailbox scoped", "Archive", []string{"quarterly"}, map[string][]uint32{"Archive": {7}}},
		{"case-insensitive", "", []string{"PIZZA"}, map[string][]uint32{"INBOX": {2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := b.SearchText("acc", tt.mailbox, tt.words)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
			for mbox, uids := range tt.want {
				for _, uid := range uids {
					if !got[mbox][uid] {
						t.Fatalf("missing %s/%d in %v", mbox, uid, got)
					}
				}
			}
		})
	}
}

func TestSQLite_SearchText_FollowsUpdatesAndDeletes(t *testing.T) {
	b := NewSQLiteBackend(t.TempDir())
	defer b.Close()
	emails := ftsEmails()
	if err := b.SaveEmails("acc", "INBOX", emails); err != nil {
		t.Fatal(err)
	}
	emails[2].Body = "sushi instead"
	delete(emails, 1)
	if err := b.SaveEmails("acc", "INBOX", emails); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.SearchText("acc", "", []string{"pizza"}); len(got) != 0 {
		t.Fatalf("stale body still indexed: %v", got)
	}
	if got, _ := b.SearchText("acc", "", []string{"sushi"}); !got["INBOX"][2] {
		t.Fatalf("updated body not indexed: %v", got)
	}
	if got, _ := b.SearchText("acc", "", []string{"quarterly"}); len(got) != 0 {
		t.Fatalf("deleted row still indexed: %v", got)
	}
}

func TestSQLite_SearchText_IndexesExistingRowsOnUpgrade(t *testing.T) {
	dir := t.TempDir()
	b := NewSQLiteBackend(dir)
	if err := b.SaveEmails("acc", "INBOX", ftsEmails()); err != nil {
		t.Fatal(err)
	}
	db, _ := b.getDB("acc")
	if _, err := db.Exec("DROP TABLE emails_fts"); err != nil {
		t.Fatal(err)
	}
	b.Close()

	// A fresh open must rebuild the index from the existing rows.
	b2 := NewSQLiteBackend(dir)
	defer b2.Close()
	got, err := b2.SearchText("acc", "", []string{"quarterly"})
	if err != nil {
		t.Fatal(err)
	}
	if !got["INBOX"][1] {
		t.Fatalf("index not rebuilt from existing rows: %v", got)
	}
}
