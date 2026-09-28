package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-message"

	"github.com/golang-mail/go-mail/cache"
)

// Google Calendar records an RSVP only when the attendee, signed in to
// Google, opens the tokenized RESPOND link from the invite's HTML part.
// An iTIP reply by mail is ignored unless the organizer reads mail in Gmail.
var googleRespondRE = regexp.MustCompile(`https://calendar\.google\.com/calendar/event\?[^"'\s<>]*action=RESPOND[^"'\s<>]*`)

var googleRSTResponses = map[string]string{"1": "ACCEPTED", "2": "DECLINED", "3": "TENTATIVE"}

// rsvpFetchRaw downloads the full message; tests swap it out.
var rsvpFetchRaw = func(acc *cache.Account, mailbox string, uid uint32) ([]byte, error) {
	c, err := acc.Acquire()
	if err != nil {
		return nil, err
	}
	defer acc.Release(c)
	if _, err := c.Select(mailbox, true); err != nil {
		return nil, err
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(seqset, []imap.FetchItem{"BODY.PEEK[]"}, messages) }()
	msg := firstMessage(messages)
	if err := <-done; err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, errors.New("message not found")
	}
	for _, literal := range msg.Body {
		if literal != nil {
			return io.ReadAll(literal)
		}
	}
	return nil, errors.New("message has no body")
}

// googleRSVPLinks maps ACCEPTED/DECLINED/TENTATIVE to the RESPOND links
// found in the message's HTML parts; empty for non-Google invites.
func googleRSVPLinks(raw []byte) map[string]string {
	links := map[string]string{}
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return links
	}
	collectHTMLLinks(entity, links)
	return links
}

func collectHTMLLinks(e *message.Entity, links map[string]string) {
	if mr := e.MultipartReader(); mr != nil {
		for {
			p, err := mr.NextPart()
			if err != nil {
				return
			}
			collectHTMLLinks(p, links)
		}
	}
	ct, _, _ := e.Header.ContentType()
	if !strings.HasPrefix(ct, "text/html") {
		return
	}
	b, err := io.ReadAll(e.Body)
	if err != nil {
		return
	}
	for _, m := range googleRespondRE.FindAllString(string(b), -1) {
		u := html.UnescapeString(m)
		if resp, ok := googleRSTResponses[queryParam(u, "rst")]; ok && links[resp] == "" {
			links[resp] = u
		}
	}
}

func queryParam(u, key string) string {
	_, q, _ := strings.Cut(u, "?")
	for _, kv := range strings.Split(q, "&") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func rsvpLinksHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		uid64, err := strconv.ParseUint(q.Get("uid"), 10, 32)
		if err != nil {
			http.Error(w, "invalid uid", http.StatusBadRequest)
			return
		}
		store.Mu.RLock()
		acc, _, email := findEmail(store, q.Get("account"), q.Get("mailbox"), uint32(uid64))
		var contentType string
		if email != nil {
			contentType = email.ContentType
		}
		store.Mu.RUnlock()
		if email == nil {
			http.Error(w, "Email not found", http.StatusNotFound)
			return
		}
		if !strings.HasPrefix(contentType, "text/calendar") {
			http.Error(w, "not a calendar invite", http.StatusUnprocessableEntity)
			return
		}
		raw, err := rsvpFetchRaw(acc, q.Get("mailbox"), uint32(uid64))
		if err != nil {
			slog.Error("failed to fetch invite for RSVP links", "account", acc.Name, "mailbox", q.Get("mailbox"), "uid", uid64, "error", err)
			http.Error(w, "failed to fetch message: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(googleRSVPLinks(raw))
	}
}
