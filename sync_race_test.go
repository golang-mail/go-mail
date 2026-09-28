package main

import (
	"sync"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

// Delete/move handlers shrink the mailbox map under the write lock while a
// sync is walking it; the walk must hold the lock or the runtime panics.
func TestExistingEmailsForSync_SafeAgainstConcurrentDelete(t *testing.T) {
	emails := map[uint32]*cache.CachedEmail{}
	serverUIDs := map[uint32]bool{}
	serverFlags := map[uint32][]string{}
	for uid := uint32(1); uid <= 2000; uid++ {
		emails[uid] = testEmail("x", true)
		serverUIDs[uid] = true
		serverFlags[uid] = []string{"\\Seen"}
	}
	store, account := newFilterTestStore(t, emails)
	mbox := account.Mailboxes["INBOX"]

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for uid := uint32(1); uid <= 2000; uid++ {
			store.Mu.Lock()
			delete(mbox.Emails, uid)
			store.Mu.Unlock()
		}
	}()
	var got map[uint32]*cache.CachedEmail
	go func() {
		defer wg.Done()
		got, _ = existingEmailsForSync(store, account, "INBOX", serverUIDs, serverFlags)
	}()
	wg.Wait()

	for uid, e := range got {
		if len(e.Flags) != 1 || e.Flags[0] != "\\Seen" {
			t.Fatalf("uid %d: flags not refreshed: %v", uid, e.Flags)
		}
	}
}

func TestExistingEmailsForSync_DropsEmailsGoneFromServer(t *testing.T) {
	emails := map[uint32]*cache.CachedEmail{1: testEmail("keep", true), 2: testEmail("gone", true)}
	store, account := newFilterTestStore(t, emails)
	got, filtered := existingEmailsForSync(store, account, "INBOX", map[uint32]bool{1: true}, map[uint32][]string{1: {"\\Flagged"}})
	if len(got) != 1 || got[1] == nil {
		t.Fatalf("want only uid 1, got %v", got)
	}
	if len(got[1].Flags) != 1 || got[1].Flags[0] != "\\Flagged" {
		t.Fatalf("flags not refreshed: %v", got[1].Flags)
	}
	if !filtered[1] {
		t.Fatalf("want filtered snapshot for uid 1, got %v", filtered)
	}
	empty, _ := existingEmailsForSync(store, account, "MISSING", nil, nil)
	if empty == nil {
		t.Fatal("want empty map for unknown mailbox, got nil")
	}
}

// A body backfill must not read live email state outside the store lock: a
// filter or handler can flip Filtered on the same object while the sync runs.
func TestSyncMailbox_BackfillReadsFilteredUnderLock(t *testing.T) {
	ts, store, acc := serverBackedStore(t)
	uid := deliver(t, ts, "INBOX", "backfill me")
	syncNow(t, store, acc, "INBOX")

	store.Mu.Lock()
	email := acc.Mailboxes["INBOX"].Emails[uid]
	email.Body = "" // force a body backfill on the next sync
	email.Filtered = true
	store.Mu.Unlock()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			store.Mu.Lock()
			email.Filtered = !email.Filtered
			store.Mu.Unlock()
		}
	}()

	for i := 0; i < 5; i++ {
		syncNow(t, store, acc, "INBOX")
	}
	close(stop)
	wg.Wait()
}
