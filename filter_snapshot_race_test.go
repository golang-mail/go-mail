package main

import (
	"testing"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/filter"
)

// Handlers and sync change flags and labels under the store write lock while
// filters are matching. Matching must work on a snapshot, not the live email.
func TestFilterMatchingDoesNotRaceWithHandlerWrites(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })

	emails := map[uint32]*cache.CachedEmail{}
	for uid := uint32(1); uid <= 50; uid++ {
		e := testEmail("snapshot me", false)
		e.Labels = []string{"inbox"}
		emails[uid] = e
	}
	store, account := newFilterTestStore(t, emails)

	cfg := filter.Config{Filters: []filter.Filter{{
		ID:        "st",
		Name:      "Unread with label",
		Mailboxes: []string{"INBOX"},
		Conditions: []filter.Condition{
			{Field: "status", Operator: "equals", Value: "unread"},
			{Field: "label", Operator: "contains", Value: "inbox"},
		},
		Actions: []filter.Action{{Type: "label", Value: "work"}},
	}}}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			store.Mu.Lock()
			for _, e := range emails {
				if i%2 == 0 {
					e.Flags = append(e.Flags, `\Flagged`)
				} else {
					e.Flags = nil
				}
				e.Labels = append(e.Labels, "touched")
				if len(e.Labels) > 4 {
					e.Labels = e.Labels[:1]
				}
			}
			store.Mu.Unlock()
		}
	}()

	filterAccount(store, account, cfg, true, false, "", nil, false)
	close(stop)
	<-done
}
