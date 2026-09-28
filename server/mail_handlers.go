package server

import (
	"encoding/json"
	"fmt"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/metrics"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxPageSize = 500

// clampPage returns slice bounds for a 1-based page without integer overflow.
func clampPage(page, pageSize, total int) (start, end int) {
	page = max(page, 1)
	pageSize = max(pageSize, 1)
	if page-1 > total/pageSize {
		return total, total
	}
	start = (page - 1) * pageSize
	if pageSize > total-start {
		return start, total
	}
	return start, start + pageSize
}

func mailsHandler(store *cache.Store) http.HandlerFunc {
	type addressJSON struct {
		PersonalName string `json:"PersonalName"`
		MailboxName  string `json:"MailboxName"`
		HostName     string `json:"HostName"`
	}
	type emailJSON struct {
		Subject     string        `json:"subject"`
		UID         uint32        `json:"uid"`
		From        []addressJSON `json:"from"`
		Date        time.Time     `json:"date"`
		Flags       []string      `json:"flags"`
		Attachments []string      `json:"attachments"`
		MessageID   string        `json:"messageId"`
		InReplyTo   string        `json:"inReplyTo"`
		Labels      []string      `json:"labels"`
	}
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

		emails := make([]emailJSON, 0, len(mailbox.Emails))
		for _, msg := range mailbox.Emails {
			if !msg.Filtered {
				continue
			}
			subject := msg.Env().Subject
			if subject == "" {
				subject = "[NO SUBJECT]"
			}
			fromArr := make([]addressJSON, 0, len(msg.Env().From))
			for _, addr := range msg.Env().From {
				fromArr = append(fromArr, addressJSON{
					PersonalName: addr.PersonalName,
					MailboxName:  addr.MailboxName,
					HostName:     addr.HostName,
				})
			}
			emails = append(emails, emailJSON{
				Subject:     subject,
				UID:         msg.UID,
				From:        fromArr,
				Date:        msg.Env().Date,
				Flags:       msg.Flags,
				Attachments: msg.Attachments,
				MessageID:   msg.Env().MessageID,
				InReplyTo:   msg.Env().InReplyTo,
				Labels:      msg.Labels,
			})
		}

		sort.Slice(emails, func(i, j int) bool {
			return emails[i].Date.After(emails[j].Date)
		})

		// Group emails by conversation
		type conversation struct {
			Subject string      `json:"subject"`
			Emails  []emailJSON `json:"emails"`
			Latest  time.Time
		}
		conversations := make(map[string]*conversation)
		for _, e := range emails {
			normalizedSubject := strings.TrimPrefix(strings.ToLower(e.Subject), "re: ")
			if normalizedSubject == "" {
				// Don't group emails with no subject together
				conversations[fmt.Sprintf("__no_subject_%d", e.UID)] = &conversation{
					Subject: e.Subject,
					Emails:  []emailJSON{e},
					Latest:  e.Date,
				}
				continue
			}
			if conv, ok := conversations[normalizedSubject]; ok {
				conv.Emails = append(conv.Emails, e)
				if e.Date.After(conv.Latest) {
					conv.Latest = e.Date
				}
			} else {
				conversations[normalizedSubject] = &conversation{
					Subject: e.Subject,
					Emails:  []emailJSON{e},
					Latest:  e.Date,
				}
			}
		}

		// Filter by unread if requested
		unreadOnly := r.URL.Query().Get("unread") == "true"
		var sortedConversations []*conversation
		for _, conv := range conversations {
			if unreadOnly {
				hasUnread := false
				for _, e := range conv.Emails {
					seen := false
					for _, f := range e.Flags {
						if f == imap.SeenFlag {
							seen = true
							break
						}
					}
					if !seen {
						hasUnread = true
						break
					}
				}
				if !hasUnread {
					continue
				}
			}
			sortedConversations = append(sortedConversations, conv)
		}

		sort.Slice(sortedConversations, func(i, j int) bool {
			return sortedConversations[i].Latest.After(sortedConversations[j].Latest)
		})

		// Pagination
		totalConversations := len(sortedConversations)
		pageSize := 100
		page := 1
		if ps := r.URL.Query().Get("pageSize"); ps != "" {
			if v, err := strconv.Atoi(ps); err == nil && v > 0 {
				pageSize = min(v, maxPageSize)
			}
		}
		if p := r.URL.Query().Get("page"); p != "" {
			if v, err := strconv.Atoi(p); err == nil && v > 0 {
				page = v
			}
		}
		start, end := clampPage(page, pageSize, totalConversations)
		paged := sortedConversations[start:end]

		type pagedResponse struct {
			Conversations []*conversation `json:"conversations"`
			Total         int             `json:"total"`
			Page          int             `json:"page"`
			PageSize      int             `json:"pageSize"`
		}

		writeJSONWithETag(w, r, pagedResponse{
			Conversations: paged,
			Total:         totalConversations,
			Page:          page,
			PageSize:      pageSize,
		})
	}
}

func markAsSeenHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
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

		uidStr := r.URL.Query().Get("uid")
		if uidStr == "" {
			http.Error(w, "uid query parameter is required", http.StatusBadRequest)
			return
		}
		uid, err := strconv.ParseUint(uidStr, 10, 32)
		if err != nil {
			http.Error(w, "invalid uid", http.StatusBadRequest)
			return
		}

		// IMAP first, cache second: a failed flag store must not diverge.
		store.Mu.RLock()
		acc, _, email := findEmail(store, accountName, mailboxName, uint32(uid))
		needsChange := email != nil && !hasSeenFlag(email.Flags)
		store.Mu.RUnlock()
		if email == nil {
			http.Error(w, "Mail not found", http.StatusNotFound)
			return
		}
		if !needsChange {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := withMailboxSync(acc, mailboxName, "mark as seen", func(c *client.Client) error {
			return storeFlags(c, []uint32{uint32(uid)}, imap.AddFlags, imap.SeenFlag)
		}); err != nil {
			slog.Error("failed to mark as seen", "account", acc.Name, "mailbox", mailboxName, "uid", uid, "error", err)
			http.Error(w, "failed to mark mail as seen on the server", http.StatusBadGateway)
			return
		}
		store.Mu.Lock()
		if acc2, mailbox, email := findEmail(store, accountName, mailboxName, uint32(uid)); email != nil {
			store.MarkDirty(acc2.Name, mailbox.Name)
			if !hasSeenFlag(email.Flags) {
				email.Flags = append(email.Flags, imap.SeenFlag)
				if mailbox.Unseen > 0 {
					mailbox.Unseen--
				}
			}
		}
		store.Mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
}

func markAsUnseenHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		accountName := r.URL.Query().Get("account")
		mailboxName := r.URL.Query().Get("mailbox")
		uidStr := r.URL.Query().Get("uid")
		uid, err := strconv.ParseUint(uidStr, 10, 32)
		if err != nil {
			http.Error(w, "invalid uid", http.StatusBadRequest)
			return
		}

		// IMAP first, cache second: a failed flag store must not diverge.
		store.Mu.RLock()
		acc, _, email := findEmail(store, accountName, mailboxName, uint32(uid))
		needsChange := email != nil && hasSeenFlag(email.Flags)
		store.Mu.RUnlock()
		if email == nil {
			http.Error(w, "Mail not found", http.StatusNotFound)
			return
		}
		if !needsChange {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := withMailboxSync(acc, mailboxName, "mark as unseen", func(c *client.Client) error {
			return storeFlags(c, []uint32{uint32(uid)}, imap.RemoveFlags, imap.SeenFlag)
		}); err != nil {
			slog.Error("failed to mark as unseen", "account", acc.Name, "mailbox", mailboxName, "uid", uid, "error", err)
			http.Error(w, "failed to mark mail as unseen on the server", http.StatusBadGateway)
			return
		}
		store.Mu.Lock()
		if acc2, mailbox, email := findEmail(store, accountName, mailboxName, uint32(uid)); email != nil {
			store.MarkDirty(acc2.Name, mailbox.Name)
			if hasSeenFlag(email.Flags) {
				newFlags := make([]string, 0, len(email.Flags))
				for _, flag := range email.Flags {
					if flag != imap.SeenFlag {
						newFlags = append(newFlags, flag)
					}
				}
				email.Flags = newFlags
				mailbox.Unseen++
			}
		}
		store.Mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
}

func mailHandler(store *cache.Store) http.HandlerFunc {
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

		uidStr := r.URL.Query().Get("uid")
		if uidStr == "" {
			http.Error(w, "uid query parameter is required", http.StatusBadRequest)
			return
		}
		uid, err := strconv.ParseUint(uidStr, 10, 32)
		if err != nil {
			http.Error(w, "invalid uid", http.StatusBadRequest)
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
		msg, ok := mailbox.Emails[uint32(uid)]
		if !ok || !msg.Filtered {
			http.Error(w, "Mail not found", http.StatusNotFound)
			return
		}
		slog.Debug("found message", "found_uid", msg.UID, "requested_uid", uint32(uid))
		w.Header().Set("Content-Type", "application/json")

		subject := msg.Env().Subject
		if subject == "" {
			subject = "[NO SUBJECT]"
		}
		// Build response object with address normalization (both camelCase and PascalCase keys)
		buildAddrs := func(addrs []*cache.Address) []map[string]interface{} {
			var out []map[string]interface{}
			for _, a := range addrs {
				out = append(out, map[string]interface{}{
					"personalName": a.PersonalName,
					"mailboxName":  a.MailboxName,
					"hostName":     a.HostName,
					"PersonalName": a.PersonalName,
					"MailboxName":  a.MailboxName,
					"HostName":     a.HostName,
				})
			}
			return out
		}

		contentType := func() string {
			if strings.TrimSpace(msg.ContentType) == "" {
				lowerBody := strings.ToUpper(msg.Body)
				if strings.Contains(lowerBody, "BEGIN:VCALENDAR") || strings.Contains(lowerBody, "BEGIN:VEVENT") {
					return "text/calendar"
				}
				return ""
			}
			if strings.HasPrefix(msg.ContentType, "text/plain") {
				upperBody := strings.ToUpper(msg.Body)
				if strings.Contains(upperBody, "BEGIN:VCALENDAR") || strings.Contains(upperBody, "BEGIN:VEVENT") {
					return "text/calendar"
				}
			}
			return msg.ContentType
		}()

		resp := map[string]interface{}{
			"subject":     subject,
			"uid":         msg.UID,
			"body":        msg.Body,
			"contentType": contentType,
			"from":        buildAddrs(msg.Env().From),
			"to":          buildAddrs(msg.Env().To),
			"cc":          buildAddrs(msg.Env().Cc),
			"bcc":         buildAddrs(msg.Env().Bcc),
			"replyTo":     buildAddrs(msg.Env().ReplyTo),
			"attachments": msg.Attachments,
			"date":        msg.Env().Date,
			"headers":     msg.Headers,
			"labels":      msg.Labels,
			"flags":       msg.Flags,
		}
		json.NewEncoder(w).Encode(resp)
	}
}

// removeCachedEmail drops uid from mbox and fixes its counters; ok is false
// when the uid is absent, so counters never underflow.
func removeCachedEmail(mbox *cache.Mailbox, uid uint32) (email *cache.CachedEmail, wasUnseen, ok bool) {
	email, ok = mbox.Emails[uid]
	if !ok {
		return nil, false, false
	}
	wasUnseen = !hasSeenFlag(email.Flags)
	delete(mbox.Emails, uid)
	if mbox.Messages > 0 {
		mbox.Messages--
	}
	if wasUnseen && mbox.Unseen > 0 {
		mbox.Unseen--
	}
	return email, wasUnseen, true
}

func deleteHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		accountName := r.URL.Query().Get("account")
		mailboxName := r.URL.Query().Get("mailbox")
		uidStr := r.URL.Query().Get("uid")
		uid, err := strconv.ParseUint(uidStr, 10, 32)
		if err != nil {
			http.Error(w, "invalid uid", http.StatusBadRequest)
			return
		}

		store.Mu.Lock()
		defer store.Mu.Unlock()

		acc, from := findMailbox(store, accountName, mailboxName)
		if from == nil {
			http.Error(w, "Mailbox not found", http.StatusNotFound)
			return
		}
		store.MarkDirty(acc.Name, from.Name)
		email, isUnseen, ok := removeCachedEmail(from, uint32(uid))
		if !ok {
			http.Error(w, "Mail not found", http.StatusNotFound)
			return
		}

		moved := false
		if deleted := acc.DeletedMailbox; deleted != "" && !strings.EqualFold(mailboxName, deleted) {
			if to, ok := acc.Mailboxes[deleted]; ok {
				if to.Emails == nil {
					to.Emails = make(map[uint32]*cache.CachedEmail)
				}
				to.Emails[uint32(uid)] = email
				to.Messages++
				if isUnseen {
					to.Unseen++
				}
				go withMailbox(acc, mailboxName, "move email to deleted mailbox", func(c *client.Client) error {
					return moveUIDs(c, []uint32{uint32(uid)}, deleted)
				})
				moved = true
			} else {
				slog.Warn("deleted mailbox not found in cache, falling back to permanent delete", "mailbox", deleted, "account", acc.Name)
			}
		}
		if !moved {
			go withMailbox(acc, mailboxName, "delete email", func(c *client.Client) error {
				return deleteUIDs(c, []uint32{uint32(uid)})
			})
		}
		metrics.EmailsDeleted.Inc()
		w.WriteHeader(http.StatusOK)
	}
}

func deleteAllHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Account string   `json:"account"`
			Mailbox string   `json:"mailbox"`
			UIDs    []uint32 `json:"uids"`
			All     bool     `json:"all"`
		}
		if err := decodeJSONBody(w, r, &req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.UIDs) == 0 && !req.All {
			http.Error(w, "no uids provided", http.StatusBadRequest)
			return
		}

		uidSet := make(map[uint32]bool, len(req.UIDs))
		for _, uid := range req.UIDs {
			uidSet[uid] = true
		}

		store.Mu.Lock()
		defer store.Mu.Unlock()

		acc, from := findMailbox(store, req.Account, req.Mailbox)
		if from == nil {
			http.Error(w, "Mailbox not found", http.StatusNotFound)
			return
		}
		store.MarkDirty(acc.Name, from.Name)
		// If source is already the deleted mailbox, skip move and permanently delete
		if deleted := acc.DeletedMailbox; deleted != "" && !strings.EqualFold(req.Mailbox, deleted) {
			if to, ok := acc.Mailboxes[deleted]; ok {
				if to.Emails == nil {
					to.Emails = make(map[uint32]*cache.CachedEmail)
				}
				var uidsToMove []uint32
				for uid, email := range from.Emails {
					if !req.All && !uidSet[uid] {
						continue
					}
					// Bulk delete must spare mail the user has never seen yet.
					if req.All && !email.Filtered {
						continue
					}
					isUnseen := !hasSeenFlag(email.Flags)
					to.Emails[uid] = email
					delete(from.Emails, uid)
					if from.Messages > 0 {
						from.Messages--
					}
					if isUnseen && from.Unseen > 0 {
						from.Unseen--
					}
					to.Messages++
					if isUnseen {
						to.Unseen++
					}
					uidsToMove = append(uidsToMove, uid)
				}
				go withMailbox(acc, req.Mailbox, "move emails to deleted mailbox", func(c *client.Client) error {
					return moveUIDs(c, uidsToMove, deleted)
				})
				w.WriteHeader(http.StatusOK)
				return
			}
			slog.Warn("deleted mailbox not found in cache, falling back to permanent delete", "mailbox", deleted, "account", acc.Name)
		}
		var uidsToDelete []uint32
		for uid, email := range from.Emails {
			if !req.All && !uidSet[uid] {
				continue
			}
			// Bulk delete must spare mail the user has never seen yet.
			if req.All && !email.Filtered {
				continue
			}
			if !hasSeenFlag(email.Flags) && from.Unseen > 0 {
				from.Unseen--
			}
			delete(from.Emails, uid)
			if from.Messages > 0 {
				from.Messages--
			}
			uidsToDelete = append(uidsToDelete, uid)
		}
		go withMailbox(acc, req.Mailbox, "delete emails", func(c *client.Client) error {
			return deleteUIDs(c, uidsToDelete)
		})
		w.WriteHeader(http.StatusOK)
	}
}

func moveSelectedHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Account   string   `json:"account"`
			Mailbox   string   `json:"mailbox"`
			ToMailbox string   `json:"to_mailbox"`
			UIDs      []uint32 `json:"uids"`
			All       bool     `json:"all"`
		}
		if err := decodeJSONBody(w, r, &req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.UIDs) == 0 && !req.All {
			http.Error(w, "no uids provided", http.StatusBadRequest)
			return
		}
		if req.ToMailbox == "" {
			http.Error(w, "to_mailbox is required", http.StatusBadRequest)
			return
		}

		store.Mu.Lock()
		defer store.Mu.Unlock()

		acc := findAccount(store, req.Account)
		if acc == nil {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
		from, ok := acc.Mailboxes[req.Mailbox]
		if !ok {
			http.Error(w, "source mailbox not found", http.StatusNotFound)
			return
		}
		to, ok := acc.Mailboxes[req.ToMailbox]
		createMailbox := !ok
		if createMailbox {
			to = &cache.Mailbox{Name: req.ToMailbox}
			acc.Mailboxes[req.ToMailbox] = to
		}
		if to.Emails == nil {
			to.Emails = make(map[uint32]*cache.CachedEmail)
		}
		store.MarkDirty(acc.Name, req.Mailbox)
		store.MarkDirty(acc.Name, req.ToMailbox)

		var uidsToMove []uint32
		moveEmail := func(uid uint32, email *cache.CachedEmail) {
			to.Emails[uid] = email
			delete(from.Emails, uid)
			uidsToMove = append(uidsToMove, uid)
			if from.Messages > 0 {
				from.Messages--
			}
			isUnseen := !hasSeenFlag(email.Flags)
			if isUnseen && from.Unseen > 0 {
				from.Unseen--
			}
			to.Messages++
			if isUnseen {
				to.Unseen++
			}
		}
		if req.All {
			for uid, email := range from.Emails {
				// Bulk move must spare mail the user has never seen yet.
				if !email.Filtered {
					continue
				}
				moveEmail(uid, email)
			}
		} else {
			for _, uid := range req.UIDs {
				if email, ok := from.Emails[uid]; ok {
					moveEmail(uid, email)
				}
			}
		}

		go withMailbox(acc, req.Mailbox, "move emails", func(c *client.Client) error {
			if createMailbox {
				if err := c.Create(req.ToMailbox); err != nil {
					slog.Error("failed to create mailbox", "mailbox", req.ToMailbox, "error", err)
				}
				_ = c.Subscribe(req.ToMailbox)
			}
			return moveUIDs(c, uidsToMove, req.ToMailbox)
		})
		w.WriteHeader(http.StatusOK)
	}
}

func markAllAsReadHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Account string   `json:"account"`
			Mailbox string   `json:"mailbox"`
			UIDs    []uint32 `json:"uids"`
			All     bool     `json:"all"`
		}
		if err := decodeJSONBody(w, r, &req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.UIDs) == 0 && !req.All {
			http.Error(w, "no uids provided", http.StatusBadRequest)
			return
		}

		uidSet := make(map[uint32]bool, len(req.UIDs))
		for _, uid := range req.UIDs {
			uidSet[uid] = true
		}

		store.Mu.Lock()
		defer store.Mu.Unlock()

		acc, mailbox := findMailbox(store, req.Account, req.Mailbox)
		if mailbox == nil {
			http.Error(w, "Mailbox not found", http.StatusNotFound)
			return
		}
		store.MarkDirty(acc.Name, mailbox.Name)
		var markedUIDs []uint32
		var unseenDelta uint32
		for _, email := range mailbox.Emails {
			if !req.All && !uidSet[email.UID] {
				continue
			}
			if !hasSeenFlag(email.Flags) {
				email.Flags = append(email.Flags, imap.SeenFlag)
				unseenDelta++
			}
			markedUIDs = append(markedUIDs, email.UID)
		}
		if mailbox.Unseen >= unseenDelta {
			mailbox.Unseen -= unseenDelta
		} else {
			mailbox.Unseen = 0
		}
		go withMailbox(acc, req.Mailbox, "mark as read", func(c *client.Client) error {
			return storeFlags(c, markedUIDs, imap.AddFlags, imap.SeenFlag)
		})
		w.WriteHeader(http.StatusOK)
	}
}

func moveHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		accountName := r.URL.Query().Get("account")
		fromMailbox := r.URL.Query().Get("from")
		toMailbox := r.URL.Query().Get("to")
		uidStr := r.URL.Query().Get("uid")
		uid, err := strconv.ParseUint(uidStr, 10, 32)
		if err != nil {
			http.Error(w, "invalid uid", http.StatusBadRequest)
			return
		}

		store.Mu.Lock()
		acc, from := findMailbox(store, accountName, fromMailbox)
		var email *cache.CachedEmail
		createMailbox := false
		if from != nil {
			store.MarkDirty(acc.Name, fromMailbox)
			store.MarkDirty(acc.Name, toMailbox)
			to, ok := acc.Mailboxes[toMailbox]
			if !ok {
				to = &cache.Mailbox{Name: toMailbox, Emails: make(map[uint32]*cache.CachedEmail)}
				acc.Mailboxes[toMailbox] = to
				createMailbox = true
			}
			if to.Emails == nil {
				to.Emails = make(map[uint32]*cache.CachedEmail)
			}
			if moved, isUnseen, ok := removeCachedEmail(from, uint32(uid)); ok {
				to.Emails[uint32(uid)] = moved
				to.Messages++
				if isUnseen {
					to.Unseen++
				}
				email = moved
			}
		}
		store.Mu.Unlock()
		if email == nil {
			http.Error(w, "Mail not found", http.StatusNotFound)
			return
		}

		go withIMAP(acc, "move email", func(c *client.Client) error {
			if createMailbox {
				if err := c.Create(toMailbox); err != nil {
					slog.Error("failed to create mailbox", "mailbox", toMailbox, "error", err)
				}
				_ = c.Subscribe(toMailbox)
			}
			if _, err := c.Select(fromMailbox, false); err != nil {
				return fmt.Errorf("select %s: %w", fromMailbox, err)
			}
			if err := moveUIDs(c, []uint32{uint32(uid)}, toMailbox); err != nil {
				return err
			}
			return c.Expunge(nil)
		})
		metrics.EmailsMoved.Inc()
		w.WriteHeader(http.StatusOK)
	}
}

func hasSeenFlag(flags []string) bool {
	for _, f := range flags {
		if f == imap.SeenFlag {
			return true
		}
	}
	return false
}
