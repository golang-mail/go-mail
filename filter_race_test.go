package main

import (
	"testing"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/filter"
)

// Handlers read email fields under the store read lock; filter goroutines must
// not mutate those fields outside the store write lock.
func TestFilterLabelActionDoesNotRaceWithReaders(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })

	emails := map[uint32]*cache.CachedEmail{}
	for uid := uint32(1); uid <= 50; uid++ {
		emails[uid] = testEmail("tag me", false)
	}
	store, account := newFilterTestStore(t, emails)

	cfg := filter.Config{Filters: []filter.Filter{{
		ID:         "lbl",
		Name:       "Label work",
		Mailboxes:  []string{"INBOX"},
		Conditions: []filter.Condition{{Field: "subject", Operator: "equals", Value: "tag me"}},
		Actions:    []filter.Action{{Type: "label", Value: "work"}},
	}}}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			store.Mu.RLock()
			for _, e := range emails {
				_ = len(e.Labels)
				_ = len(e.PluginsRun)
				_ = e.Filtered
			}
			store.Mu.RUnlock()
		}
	}()

	filterAccount(store, account, cfg, true, false, "", nil, false)
	close(stop)
	<-done

	for uid, e := range emails {
		if len(e.Labels) != 1 || e.Labels[0] != "work" {
			t.Fatalf("uid %d: want labels [work], got %v", uid, e.Labels)
		}
	}
}
