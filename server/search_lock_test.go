package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

// Regex-looking free text can't use the FTS index; it must still find
// results through the snapshot path, with operators and scoping applied.
func TestSearchHandler_RegexPathMatchesOutsideLock(t *testing.T) {
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: sampleFiltered()})
	store.Accounts[0].Mailboxes["INBOX"].Emails[1].Envelope.Subject = "weekly report"
	store.Accounts[0].Mailboxes["INBOX"].Emails[1].Body = "nothing to see"
	other := sampleFiltered()
	other.UID = 2
	other.Envelope.Subject = "once upon a time"
	store.Accounts[0].Mailboxes["INBOX"].Emails[2] = other
	// No TextSearcher backend: the regex path is the only path.

	get := func(q string) []map[string]any {
		w := httptest.NewRecorder()
		searchHandler(store)(w, httptest.NewRequest(http.MethodGet, "/api/search?query="+url.QueryEscape(q), nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", q, w.Code, w.Body.String())
		}
		var out []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if got := get("w..kly"); len(got) != 1 || got[0]["subject"] != "weekly report" {
		t.Fatalf("regex free text: want 1 result for 'weekly report', got %v", got)
	}
	if got := get("w..kly once"); len(got) != 0 {
		t.Fatalf("words are ANDed: want 0, got %v", got)
	}
	if got := get("is:read w..kly"); len(got) != 0 {
		t.Fatalf("operator must combine with the regex scan: uid 1 is unread, got %v", got)
	}
	if got := get(`(?s)no.hing to see`); len(got) != 1 {
		t.Fatalf("body must be scanned: want 1, got %v", got)
	}
	if got := get("once"); len(got) != 1 {
		t.Fatalf("plain word without an index backend must still scan: want 1, got %v", got)
	}
}
