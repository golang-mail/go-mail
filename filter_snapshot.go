package main

import "github.com/golang-mail/go-mail/cache"

// snapshotEmails copies each email under the read lock so filter matching
// can run lock-free while handlers and sync keep writing flags and labels.
func snapshotEmails(store *cache.Store, emails map[uint32]*cache.CachedEmail) map[uint32]*cache.CachedEmail {
	store.Mu.RLock()
	defer store.Mu.RUnlock()
	out := make(map[uint32]*cache.CachedEmail, len(emails))
	for uid, e := range emails {
		out[uid] = e.Snapshot()
	}
	return out
}
