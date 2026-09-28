package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestFreeTextLooksLikeRegex(t *testing.T) {
	tests := map[string]bool{
		"quarterly":        false,
		"quarterly report": false,
		"rep.rt":           true,
		"^hello":           true,
		"a|b":              true,
		"(?s)x":            true,
		"foo*":             true,
		"price 10":         false,
	}
	for q, want := range tests {
		if got := freeTextLooksLikeRegex(q); got != want {
			t.Errorf("freeTextLooksLikeRegex(%q) = %v want %v", q, got, want)
		}
	}
}

func TestParseQueryKeepsFreeWords(t *testing.T) {
	spec, err := parseQuery(`from:boss quarterly "sprint notes"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.freeWords) != 2 || spec.freeWords[0] != "quarterly" || spec.freeWords[1] != "sprint notes" {
		t.Fatalf("freeWords = %v", spec.freeWords)
	}
}

// With a sqlite backend the handler answers plain-word queries from the index
// and still honours the other operators and the mailbox scope.
func TestSearchHandler_UsesTextIndexOnSQLite(t *testing.T) {
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: sampleFiltered()})
	store.Accounts[0].Mailboxes["INBOX"].Emails[1].Envelope.Subject = "Quarterly planning"
	store.Accounts[0].Mailboxes["INBOX"].Emails[1].Body = "agenda attached"
	second := sampleFiltered()
	second.UID = 2
	second.Envelope.Subject = "Quarterly lunch"
	second.Flags = []string{`\Seen`}
	store.Accounts[0].Mailboxes["INBOX"].Emails[2] = second
	b := cache.NewSQLiteBackend(t.TempDir())
	defer b.Close()
	store.Backend = b
	store.MarkDirty("acc", "INBOX")
	store.SaveCache()

	get := func(q string) []map[string]any {
		w := httptest.NewRecorder()
		searchHandler(store)(w, httptest.NewRequest(http.MethodGet, "/api/search?account=acc&query="+q, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", q, w.Code, w.Body.String())
		}
		var out []map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if got := get("quarterly"); len(got) != 2 {
		t.Fatalf("plain word: want 2 results got %v", got)
	}
	if got := get("quarterly+is:unread"); len(got) != 1 || got[0]["uid"].(float64) != 1 {
		t.Fatalf("index candidates must still pass the other operators: %v", got)
	}
	if got := get("agenda"); len(got) != 1 {
		t.Fatalf("body word: %v", got)
	}
	if got := get("quarter.y"); len(got) != 2 {
		t.Fatalf("regex query must still scan: %v", got)
	}
}
