package server

import (
	"container/heap"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/golang-mail/go-mail/cache"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

func mailboxHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountName := r.URL.Query().Get("account")
		if accountName == "" {
			http.Error(w, "account query parameter is required", http.StatusBadRequest)
			return
		}

		mailboxName := r.URL.Query().Get("mailbox")
		if mailboxName == "" {
			http.Error(w, "mailbox query parameter is required", http.StatusBadRequest)
			return
		}

		if r.Method == http.MethodPost {
			store.Mu.RLock()
			acc := findAccount(store, accountName)
			exists := acc != nil && acc.Mailboxes[mailboxName] != nil
			store.Mu.RUnlock()
			if acc == nil {
				http.Error(w, "Account not found", http.StatusNotFound)
				return
			}
			if exists {
				w.WriteHeader(http.StatusOK)
				return
			}
			// IMAP round trip happens with the store unlocked.
			c, err := acc.Acquire()
			if err != nil {
				http.Error(w, "Failed to connect to IMAP server", http.StatusInternalServerError)
				return
			}
			defer acc.Release(c)
			created, err := cache.EnsureMailbox(c, mailboxName)
			if err != nil {
				http.Error(w, fmt.Sprintf("Failed to create mailbox: %v", err), http.StatusInternalServerError)
				return
			}
			mailboxName = created
			_ = c.Subscribe(mailboxName)
			store.Mu.Lock()
			// A concurrent request or sync may have added it meanwhile.
			if _, ok := acc.Mailboxes[mailboxName]; !ok {
				acc.Mailboxes[mailboxName] = &cache.Mailbox{
					Name:   mailboxName,
					Emails: make(map[uint32]*cache.CachedEmail),
				}
			}
			store.Mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(struct {
				Name string `json:"name"`
			}{Name: mailboxName})
			return
		}

		if r.Method == http.MethodDelete {
			store.Mu.Lock()
			defer store.Mu.Unlock()
			acc, mailbox := findMailbox(store, accountName, mailboxName)
			if mailbox == nil {
				http.Error(w, "Account or mailbox not found", http.StatusNotFound)
				return
			}
			if mailbox.Messages > 0 {
				http.Error(w, "Mailbox is not empty", http.StatusBadRequest)
				return
			}
			delete(acc.Mailboxes, mailboxName)
			go withIMAP(acc, "delete mailbox on server", func(c *client.Client) error {
				return c.Delete(mailboxName)
			})
			w.WriteHeader(http.StatusOK)
			return
		}

		store.Mu.RLock()
		defer store.Mu.RUnlock()
		acc := findAccount(store, accountName)
		if acc == nil {
			http.Error(w, "Account not found", http.StatusNotFound)
			return
		}
		mailbox, ok := acc.Mailboxes[mailboxName]
		if !ok {
			http.Error(w, "Mailbox not found", http.StatusNotFound)
			return
		}
		type mailboxStatus struct {
			Messages uint32 `json:"Messages"`
			Unseen   uint32 `json:"Unseen"`
		}
		// Count only filtered emails so the displayed total matches
		// what the mail list actually shows.
		var filteredMessages, filteredUnseen uint32
		for _, email := range mailbox.Emails {
			if !email.Filtered {
				continue
			}
			filteredMessages++
			seen := false
			for _, f := range email.Flags {
				if f == imap.SeenFlag {
					seen = true
					break
				}
			}
			if !seen {
				filteredUnseen++
			}
		}
		status := mailboxStatus{
			Messages: filteredMessages,
			Unseen:   filteredUnseen,
		}
		writeJSONWithETag(w, r, status)
	}
}

func accountsHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store.Mu.RLock()
		defer store.Mu.RUnlock()

		type accountWithUnseen struct {
			Name                   string                    `json:"Name"`
			Server                 string                    `json:"Server"`
			Port                   string                    `json:"Port"`
			Username               string                    `json:"Username"`
			TLS                    string                    `json:"TLS"`
			Unseen                 uint32                    `json:"Unseen"`
			DraftMailbox           string                    `json:"draft_mailbox,omitempty"`
			Avatar                 string                    `json:"avatar,omitempty"`
			GravatarURL            string                    `json:"gravatar_url,omitempty"`
			FromAddresses          []cache.FromAddress       `json:"from_addresses,omitempty"`
			SignatureHTML          string                    `json:"signature_html,omitempty"`
			SignatureTxt           string                    `json:"signature_txt,omitempty"`
			SilenceUnseenMailboxes []string                  `json:"silence_unseen_mailboxes,omitempty"`
			Notifications          cache.NotificationsConfig `json:"notifications"`
		}

		result := []accountWithUnseen{}
		for _, data := range store.Accounts {
			silenced := make(map[string]bool, len(data.Silence.UnseenNotifications))
			for _, name := range data.Silence.UnseenNotifications {
				silenced[name] = true
			}
			// Mailbox.Unseen is maintained incrementally by every flag/move/delete
			// handler and by sync/filter, so summing is O(mailboxes), not O(emails).
			var unseenCount uint32
			for name, mb := range data.Mailboxes {
				if silenced[name] {
					continue
				}
				unseenCount += mb.Unseen
			}
			// Compute Gravatar URL: explicit gravatar email > first from_address email
			var gravatarURL string
			gravatarEmail := data.Gravatar
			if gravatarEmail == "" && len(data.FromAddrs) > 0 {
				gravatarEmail = data.FromAddrs[0].Email
			}
			if gravatarEmail != "" {
				email := strings.TrimSpace(strings.ToLower(gravatarEmail))
				hash := fmt.Sprintf("%x", md5.Sum([]byte(email)))
				gravatarURL = "https://www.gravatar.com/avatar/" + hash + "?s=64&d=404"
			}
			// Resolve local avatar paths to served URLs
			avatarURL := data.Avatar
			if avatarURL != "" && !strings.HasPrefix(avatarURL, "http://") && !strings.HasPrefix(avatarURL, "https://") && !strings.HasPrefix(avatarURL, "/") {
				avatarURL = "/static/" + avatarURL
			}
			result = append(result, accountWithUnseen{
				Name:                   data.Name,
				Server:                 data.Server,
				Port:                   data.Port,
				Username:               data.Username,
				TLS:                    data.TLS,
				Unseen:                 unseenCount,
				DraftMailbox:           data.DraftMailbox,
				Avatar:                 avatarURL,
				GravatarURL:            gravatarURL,
				FromAddresses:          data.FromAddrs,
				SignatureHTML:          data.SignatureHTML,
				SignatureTxt:           data.SignatureTxt,
				SilenceUnseenMailboxes: data.Silence.UnseenNotifications,
				Notifications:          data.Notifications,
			})
		}
		writeJSONWithETag(w, r, result)
	}
}

func unseenSummaryHandler(store *cache.Store, filtersHaveRun *atomic.Bool) http.HandlerFunc {
	type unseenEmail struct {
		Account string `json:"account"`
		Mailbox string `json:"mailbox"`
		UID     uint32 `json:"uid"`
		Subject string `json:"subject"`
		From    string `json:"from"`
		Snippet string `json:"snippet"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// Return empty list until first filter run completes
		if !filtersHaveRun.Load() {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]unseenEmail{})
			return
		}
		accountName := r.URL.Query().Get("account")
		store.Mu.RLock()
		defer store.Mu.RUnlock()

		// Min-heap keyed by envelope date — UIDs are per-mailbox, so they
		// can't rank mail across mailboxes. Snippet extraction is deferred
		// until after the top-N is picked so we don't strip HTML for emails
		// that won't be returned.
		const limit = 20
		var h dateMinHeap
		for i := range store.Accounts {
			acc := store.Accounts[i]
			if accountName != "" && acc.Name != accountName {
				continue
			}
			silenced := make(map[string]bool, len(acc.Silence.UnseenNotifications))
			for _, name := range acc.Silence.UnseenNotifications {
				silenced[name] = true
			}
			for mbName, mb := range acc.Mailboxes {
				if silenced[mbName] {
					continue
				}
				for _, msg := range mb.Emails {
					if !msg.Filtered {
						continue
					}
					seen := false
					for _, f := range msg.Flags {
						if f == "\\Seen" {
							seen = true
							break
						}
					}
					if seen {
						continue
					}
					entry := dateEntry{date: msg.Env().Date, uid: msg.UID, cand: &unseenCandidate{acc: acc, mb: mbName, msg: msg}}
					if h.Len() < limit {
						heap.Push(&h, entry)
					} else if h[0].before(entry) {
						h[0] = entry
						heap.Fix(&h, 0)
					}
				}
			}
		}
		// Drain heap into a newest-first slice and materialize the response.
		entries := make([]dateEntry, h.Len())
		for i := len(entries) - 1; i >= 0; i-- {
			entries[i] = heap.Pop(&h).(dateEntry)
		}
		result := make([]unseenEmail, 0, len(entries))
		for _, e := range entries {
			cand := e.cand
			msg := cand.msg
			subject := msg.Env().Subject
			if subject == "" {
				subject = "[No Subject]"
			}
			from := ""
			if len(msg.Env().From) > 0 {
				addr := msg.Env().From[0]
				if addr.PersonalName != "" {
					from = addr.PersonalName
				} else {
					from = addr.MailboxName + "@" + addr.HostName
				}
			}
			result = append(result, unseenEmail{
				Account: cand.acc.Name,
				Mailbox: cand.mb,
				UID:     msg.UID,
				Subject: subject,
				From:    from,
				Snippet: extractSnippet(msg.Body),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

// unseenCandidate holds enough info to materialize an unseenEmail later,
// once we know which top-N entries to keep. Stored by pointer in the heap.
type unseenCandidate struct {
	acc *cache.Account
	mb  string
	msg *cache.CachedEmail
}

// dateEntry / dateMinHeap implement container/heap.Interface for keeping the
// top-N unseen emails by envelope date. It's a min-heap so the oldest entry
// is at root and gets evicted when a newer candidate appears.
type dateEntry struct {
	date time.Time
	uid  uint32
	cand *unseenCandidate
}

// before orders by date, then by UID so equal dates rank deterministically.
func (e dateEntry) before(o dateEntry) bool {
	if e.date.Equal(o.date) {
		return e.uid < o.uid
	}
	return e.date.Before(o.date)
}

type dateMinHeap []dateEntry

func (h dateMinHeap) Len() int { return len(h) }

func (h dateMinHeap) Less(i, j int) bool { return h[i].before(h[j]) }

func (h dateMinHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *dateMinHeap) Push(x any) { *h = append(*h, x.(dateEntry)) }

func (h *dateMinHeap) Pop() any { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }

func mailboxesHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountName := r.URL.Query().Get("account")
		if accountName == "" {
			http.Error(w, "account query parameter is required", http.StatusBadRequest)
			return
		}
		store.Mu.RLock()
		defer store.Mu.RUnlock()
		acc := findAccount(store, accountName)
		if acc == nil {
			http.Error(w, "Account not found", http.StatusNotFound)
			return
		}
		mailboxNames := make([]string, 0, len(acc.Mailboxes))
		for name := range acc.Mailboxes {
			mailboxNames = append(mailboxNames, name)
		}
		type mailboxesResponse struct {
			Mailboxes []string `json:"mailboxes"`
			Order     []string `json:"order,omitempty"`
		}
		resp := mailboxesResponse{
			Mailboxes: mailboxNames,
			Order:     acc.MailboxCfg.Order,
		}
		writeJSONWithETag(w, r, resp)
	}
}
