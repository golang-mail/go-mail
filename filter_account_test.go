package main

import (
	"testing"
	"time"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/filter"
)

func newFilterTestStore(t *testing.T, emails map[uint32]*cache.CachedEmail) (*cache.Store, *cache.Account) {
	t.Helper()
	store := cache.NewStore()
	store.Backend = cache.NewMemoryBackend(t.TempDir())
	store.Accounts = []*cache.Account{{
		Name: "acc",
		Mailboxes: map[string]*cache.Mailbox{
			"INBOX": {Name: "INBOX", Messages: uint32(len(emails)), Emails: emails},
		},
	}}
	return store, store.Accounts[0]
}

func testEmail(subject string, filtered bool) *cache.CachedEmail {
	return &cache.CachedEmail{
		Envelope: &cache.Envelope{
			Date:    time.Now(),
			Subject: subject,
			From:    []*cache.Address{{MailboxName: "gitlab", HostName: "haproxy.com"}},
		},
		Body:     "body",
		Filtered: filtered,
	}
}

// A narrowed run (delete-old, plugin rescan) evaluates a subset of filters.yml.
// It must not stamp Filtered=true on emails that never saw the full filter set,
// otherwise new arrivals synced during the narrowed run are skipped by every
// later periodic run and sit unfiltered forever.
func TestNarrowedRunLeavesUnfilteredEmailsUnfiltered(t *testing.T) {
	emails := map[uint32]*cache.CachedEmail{
		1: testEmail("new arrival", false),
		2: testEmail("already fully filtered", true),
	}
	store, account := newFilterTestStore(t, emails)

	cfg := filter.Config{Filters: []filter.Filter{{
		ID:        "del-old",
		Name:      "Delete OLD",
		Mailboxes: []string{"INBOX"},
		Conditions: []filter.Condition{
			{Field: "subject", Operator: "equals", Value: "matches-nothing"},
		},
		Actions: []filter.Action{{Type: "delete"}},
	}}}

	filterAccount(store, account, cfg, true, true, "", nil, false)

	if emails[1].Filtered {
		t.Errorf("narrowed run stamped Filtered=true on email never evaluated against the full filter set")
	}
	if !emails[2].Filtered {
		t.Errorf("narrowed run cleared Filtered on an already fully-filtered email")
	}
}

// Narrowed runs still evaluate already-filtered emails (delete-old must
// re-check age on everything), applying actions without touching Filtered.
func TestNarrowedRunEvaluatesAlreadyFilteredEmails(t *testing.T) {
	emails := map[uint32]*cache.CachedEmail{
		1: testEmail("old junk", true),
	}
	store, account := newFilterTestStore(t, emails)

	cfg := filter.Config{Filters: []filter.Filter{{
		ID:        "mark-read",
		Name:      "Mark read",
		Mailboxes: []string{"INBOX"},
		Conditions: []filter.Condition{
			{Field: "subject", Operator: "equals", Value: "old junk"},
		},
		Actions: []filter.Action{{Type: "mark_as_read"}},
	}}}

	filterAccount(store, account, cfg, true, true, "", nil, false)

	seen := false
	for _, f := range emails[1].Flags {
		if f == "\\Seen" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("narrowed run did not evaluate an already-filtered email")
	}
}

// Full runs keep the existing behavior: evaluated emails are stamped Filtered=true.
func TestFullRunStampsFiltered(t *testing.T) {
	emails := map[uint32]*cache.CachedEmail{
		1: testEmail("new arrival", false),
	}
	store, account := newFilterTestStore(t, emails)

	cfg := filter.Config{Filters: []filter.Filter{{
		ID:        "noop",
		Name:      "No match",
		Mailboxes: []string{"INBOX"},
		Conditions: []filter.Condition{
			{Field: "subject", Operator: "equals", Value: "matches-nothing"},
		},
		Actions: []filter.Action{{Type: "delete"}},
	}}}

	filterAccount(store, account, cfg, false, false, "", nil, false)

	if !emails[1].Filtered {
		t.Errorf("full run did not stamp Filtered=true on evaluated email")
	}
}
