package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

// The toggle marks one email, not the mailbox: the save must persist exactly
// that row through the hinted path (no whole-mailbox re-hash).
func TestMailLabelHandler_PersistsSingleRow(t *testing.T) {
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: sampleFiltered()})
	other := sampleFiltered()
	other.UID = 2
	other.Labels = nil
	store.Accounts[0].Mailboxes["INBOX"].Emails[1].Labels = nil
	store.Accounts[0].Mailboxes["INBOX"].Emails[2] = other
	b := cache.NewSQLiteBackend(t.TempDir())
	defer b.Close()
	store.Backend = b
	store.MarkDirty("acc", "INBOX")
	store.SaveCache()

	body := `{"account":"acc","mailbox":"INBOX","uid":1,"label":"work","action":"add"}`
	w := httptest.NewRecorder()
	mailLabelHandler(store)(w, httptest.NewRequest(http.MethodPost, "/api/mail/label", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	loaded, err := b.LoadEmails("acc", "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded[1].Labels) != 1 || loaded[1].Labels[0] != "work" {
		t.Fatalf("hinted save must persist the toggled label, got %v", loaded[1].Labels)
	}
	if len(loaded[2].Labels) != 0 {
		t.Fatalf("untouched row must be unaffected, got %v", loaded[2].Labels)
	}
}
