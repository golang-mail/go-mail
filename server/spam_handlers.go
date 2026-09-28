package server

import (
	"encoding/json"
	"github.com/golang-mail/go-mail/bayesian"
	"github.com/golang-mail/go-mail/cache"
	"log/slog"
	"net/http"
	"strconv"
)

// SpamMessage builds the classifier's view of a cached email. Headers are
// shared, never copied: the cache never mutates them in place.
func SpamMessage(email *cache.CachedEmail) bayesian.Message {
	env := email.Env()
	m := bayesian.Message{Subject: env.Subject, Body: email.Body, Headers: email.Headers}
	if len(env.From) > 0 {
		m.From = formatAddress(env.From[0])
	}
	if len(env.ReplyTo) > 0 {
		m.ReplyTo = formatAddress(env.ReplyTo[0])
	}
	return m
}

func formatAddress(a *cache.Address) string {
	if a == nil {
		return ""
	}
	addr := a.MailboxName + "@" + a.HostName
	if a.PersonalName != "" {
		return a.PersonalName + " <" + addr + ">"
	}
	return addr
}

// spamTrainHandler trains the Bayesian classifier by marking an email as spam or ham.
// POST /api/spam/train  {account, mailbox, uid, spam: true/false}
func spamTrainHandler(store *cache.Store, classifier *bayesian.Classifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Account string `json:"account"`
			Mailbox string `json:"mailbox"`
			UID     uint32 `json:"uid"`
			Spam    bool   `json:"spam"`
		}
		if err := decodeJSONBody(w, r, &body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if body.Account == "" || body.Mailbox == "" || body.UID == 0 {
			http.Error(w, "account, mailbox, and uid are required", http.StatusBadRequest)
			return
		}

		// Find the email in cache.
		store.Mu.RLock()
		var msg bayesian.Message
		found := false
		if _, _, email := findEmail(store, body.Account, body.Mailbox, body.UID); email != nil {
			msg = SpamMessage(email)
			found = true
		}
		store.Mu.RUnlock()

		if !found {
			http.Error(w, "email not found", http.StatusNotFound)
			return
		}

		classifier.Train(msg, body.Spam)
		if err := classifier.Save(); err != nil {
			slog.Error("failed to save spam classifier", "error", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// spamTrainBatchHandler trains the classifier with multiple emails at once.
// POST /api/spam/train-batch  {account, mailbox, uids: [...], spam: true/false}
func spamTrainBatchHandler(store *cache.Store, classifier *bayesian.Classifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Account string   `json:"account"`
			Mailbox string   `json:"mailbox"`
			UIDs    []uint32 `json:"uids"`
			Spam    bool     `json:"spam"`
			All     bool     `json:"all"`
		}
		if err := decodeJSONBody(w, r, &body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if body.Account == "" || body.Mailbox == "" || (len(body.UIDs) == 0 && !body.All) {
			http.Error(w, "account, mailbox, and uids are required", http.StatusBadRequest)
			return
		}

		// Collect email text under read lock.
		store.Mu.RLock()
		var emails []bayesian.Message
		if _, mb := findMailbox(store, body.Account, body.Mailbox); mb != nil {
			if body.All {
				for _, email := range mb.Emails {
					emails = append(emails, SpamMessage(email))
				}
			} else {
				for _, uid := range body.UIDs {
					if email, ok := mb.Emails[uid]; ok {
						emails = append(emails, SpamMessage(email))
					}
				}
			}
		}
		store.Mu.RUnlock()

		// Train outside lock.
		for _, e := range emails {
			classifier.Train(e, body.Spam)
		}
		if err := classifier.Save(); err != nil {
			slog.Error("failed to save spam classifier", "error", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "trained": len(emails)})
	}
}

// spamClassifyHandler returns the spam probability for an email.
// GET /api/spam/classify?account=X&mailbox=Y&uid=Z
func spamClassifyHandler(store *cache.Store, classifier *bayesian.Classifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		accountName := r.URL.Query().Get("account")
		mailbox := r.URL.Query().Get("mailbox")
		uidStr := r.URL.Query().Get("uid")
		uid, err := strconv.ParseUint(uidStr, 10, 32)
		if err != nil {
			http.Error(w, "invalid uid", http.StatusBadRequest)
			return
		}

		store.Mu.RLock()
		var msg bayesian.Message
		found := false
		if _, _, email := findEmail(store, accountName, mailbox, uint32(uid)); email != nil {
			msg = SpamMessage(email)
			found = true
		}
		store.Mu.RUnlock()

		if !found {
			http.Error(w, "email not found", http.StatusNotFound)
			return
		}

		score := classifier.Classify(msg)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"score":   score,
			"trained": classifier.IsTrained(),
		})
	}
}

// spamClassifyBatchHandler returns spam scores for all emails in a mailbox page.
// POST /api/spam/classify-batch  {account, mailbox, uids: [1,2,3]}
func spamClassifyBatchHandler(store *cache.Store, classifier *bayesian.Classifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Account string   `json:"account"`
			Mailbox string   `json:"mailbox"`
			UIDs    []uint32 `json:"uids"`
		}
		if err := decodeJSONBody(w, r, &body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		trained := classifier.IsTrained()
		scores := make(map[string]float64, len(body.UIDs))

		if trained {
			store.Mu.RLock()
			type emailMsg struct {
				uid uint32
				msg bayesian.Message
			}
			var emails []emailMsg
			if _, mb := findMailbox(store, body.Account, body.Mailbox); mb != nil {
				for _, uid := range body.UIDs {
					if email, ok := mb.Emails[uid]; ok {
						emails = append(emails, emailMsg{uid: uid, msg: SpamMessage(email)})
					}
				}
			}
			store.Mu.RUnlock()

			for _, e := range emails {
				scores[strconv.FormatUint(uint64(e.uid), 10)] = classifier.Classify(e.msg)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"scores":  scores,
			"trained": trained,
		})
	}
}

// spamStatsHandler returns Bayesian classifier statistics.
// GET /api/spam/stats
func spamStatsHandler(classifier *bayesian.Classifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(classifier.GetStats())
	}
}

// spamSafelistHandler manages the safe list of email addresses/domains that are never classified as spam.
// GET    /api/spam/safelist              — list all entries
// POST   /api/spam/safelist {entry}      — add an address or domain
// DELETE /api/spam/safelist {entry}      — remove an entry
func spamSafelistHandler(classifier *bayesian.Classifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(classifier.GetSafeList())
		case http.MethodPost:
			var body struct {
				Entry string `json:"entry"`
			}
			if err := decodeJSONBody(w, r, &body); err != nil || body.Entry == "" {
				http.Error(w, "entry is required", http.StatusBadRequest)
				return
			}
			added := classifier.AddSafe(body.Entry)
			if added {
				if err := classifier.Save(); err != nil {
					slog.Error("failed to save classifier after safelist add", "error", err)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"added": added, "entry": body.Entry})
		case http.MethodDelete:
			var body struct {
				Entry string `json:"entry"`
			}
			if err := decodeJSONBody(w, r, &body); err != nil || body.Entry == "" {
				http.Error(w, "entry is required", http.StatusBadRequest)
				return
			}
			removed := classifier.RemoveSafe(body.Entry)
			if removed {
				if err := classifier.Save(); err != nil {
					slog.Error("failed to save classifier after safelist remove", "error", err)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"removed": removed, "entry": body.Entry})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
