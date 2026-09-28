package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

// UIDs are per-mailbox, so ranking unseen mail by UID drops fresh mail whose
// mailbox restarts at low UIDs. The summary must rank by envelope date.
func TestUnseenSummary_RanksAcrossMailboxesByDate(t *testing.T) {
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(uid uint32, d time.Time, subject string) *cache.CachedEmail {
		return &cache.CachedEmail{
			UID: uid, Filtered: true,
			Envelope: &cache.Envelope{Date: d, Subject: subject},
		}
	}
	inbox := map[uint32]*cache.CachedEmail{}
	for uid := uint32(1000); uid < 1020; uid++ {
		inbox[uid] = mk(uid, old.Add(time.Duration(uid)*time.Minute), fmt.Sprintf("old %d", uid))
	}
	archive := map[uint32]*cache.CachedEmail{1: mk(1, time.Now(), "brand new")}
	store := cache.NewStore()
	store.Accounts = []*cache.Account{{
		Name: "acc",
		Mailboxes: map[string]*cache.Mailbox{
			"INBOX":   {Name: "INBOX", Messages: 20, Unseen: 20, Emails: inbox},
			"Archive": {Name: "Archive", Messages: 1, Unseen: 1, Emails: archive},
		},
	}}

	var ran atomic.Bool
	ran.Store(true)
	w := get(unseenSummaryHandler(store, &ran), "/api/unseen-summary")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var out []struct {
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 20 || out[0].Subject != "brand new" {
		t.Fatalf("newest unseen email must lead the summary, got %+v", out)
	}
}
