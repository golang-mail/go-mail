package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestParseQueryNegatedOperators(t *testing.T) {
	spec, err := parseQuery("-has:attachment -from:boss -is:unread report")
	if err != nil {
		t.Fatal(err)
	}
	withAtt := sampleEmail()
	withAtt.Attachments = []string{"a.pdf"}
	noAtt := sampleEmail()
	noAtt.Attachments = nil
	noAtt.Flags = []string{`\Seen`}
	fromBoss := sampleEmail()
	fromBoss.Attachments = nil
	fromBoss.Flags = []string{`\Seen`}
	fromBoss.Envelope.From = []*cache.Address{addr("Boss", "boss", "corp.example")}
	unread := sampleEmail()
	unread.Attachments = nil
	unread.Flags = nil
	for _, e := range []*cache.CachedEmail{withAtt, noAtt, fromBoss, unread} {
		e.Envelope.Subject = "weekly report"
	}
	if matchesSpec(withAtt, spec) {
		t.Error("-has:attachment must exclude mail with attachments")
	}
	if !matchesSpec(noAtt, spec) {
		t.Error("read mail without attachments from someone else must match")
	}
	if matchesSpec(fromBoss, spec) {
		t.Error("-from:boss must exclude the boss")
	}
	if matchesSpec(unread, spec) {
		t.Error("-is:unread must exclude unread mail")
	}
}

func TestParseQueryRegexFlagsStayCaseInsensitive(t *testing.T) {
	spec, err := parseQuery("(?s)weekly.report")
	if err != nil {
		t.Fatal(err)
	}
	if spec.freeText == nil || !spec.freeText.MatchString("WEEKLY\nREPORT") {
		t.Fatal("a query starting with a regex flag group must still be case-insensitive")
	}
}

func TestParseQueryRejectsBadOperatorValues(t *testing.T) {
	for _, q := range []string{"has:foo", "is:maybe", "older_than:soon", "newer_than:", "-has:nothing"} {
		if _, err := parseQuery(q); err == nil {
			t.Errorf("%q: want error for a known operator with a bad value", q)
		}
	}
}

func TestParseQueryRejectsOverlongFreeText(t *testing.T) {
	if _, err := parseQuery(strings.Repeat("a", maxSearchPatternLen)); err == nil {
		t.Fatal("want error for a free-text pattern over the cap")
	}
	if _, err := parseQuery(strings.Repeat("a", maxSearchPatternLen-8)); err != nil {
		t.Fatalf("a pattern under the cap must parse: %v", err)
	}
}

func TestParseQueryURLsStayFreeText(t *testing.T) {
	spec, err := parseQuery("https://example.com/path")
	if err != nil {
		t.Fatal(err)
	}
	if spec.freeText == nil || !spec.freeText.MatchString("see https://example.com/path now") {
		t.Fatal("a URL is not an operator and must search as free text")
	}
}

func TestSearchHandler_EmptyAccountSearchesAllAccounts(t *testing.T) {
	first := sampleFiltered()
	first.Envelope.Subject = "shared topic one"
	second := sampleFiltered()
	second.UID = 7
	second.Envelope.Subject = "shared topic two"
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: first})
	store.Accounts = append(store.Accounts, &cache.Account{
		Name:      "other",
		Mailboxes: map[string]*cache.Mailbox{"INBOX": {Name: "INBOX", Emails: map[uint32]*cache.CachedEmail{7: second}}},
	})

	w := httptest.NewRecorder()
	searchHandler(store)(w, httptest.NewRequest(http.MethodGet, "/api/search?query=subject:shared", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var results []struct {
		Account string `json:"account"`
		UID     uint32 `json:"uid"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("want results from both accounts, got %+v", results)
	}
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.Account] = true
	}
	if !seen["acc"] || !seen["other"] {
		t.Fatalf("results must name their account: %+v", results)
	}
}
