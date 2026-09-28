package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestSaveDraft_FilesViaIMAPAndReturnsUID(t *testing.T) {
	acc := startIMAP(t)
	acc.FromAddrs = []cache.FromAddress{{Name: "Me", Email: "me@mine.example"}}
	store := cache.NewStore()
	store.Accounts = []*cache.Account{acc}

	body := `{"account":"acc","from":{"name":"Me","email":"me@mine.example"},"to":["you@y.example"],"subject":"wip","body":"draft text"}`
	req := httptest.NewRequest(http.MethodPost, "/api/mail/draft", strings.NewReader(body))
	w := httptest.NewRecorder()
	saveDraftHandler(store)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK      bool   `json:"ok"`
		UID     uint32 `json:"uid"`
		Mailbox string `json:"mailbox"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.UID == 0 || resp.Mailbox != "Drafts" {
		t.Fatalf("response = %+v", resp)
	}

	store.Mu.RLock()
	defer store.Mu.RUnlock()
	_, drafts := findMailbox(store, "acc", "Drafts")
	if drafts == nil || drafts.Emails[resp.UID] == nil {
		t.Fatal("draft must be mirrored into the cache")
	}
	d := drafts.Emails[resp.UID]
	if d.Env().Subject != "wip" || len(d.Flags) != 1 || d.Flags[0] != `\Draft` || !d.Filtered {
		t.Fatalf("cached draft = %+v", d)
	}
	// Reopening the draft rebuilds "mailbox@host", so both halves must be cached.
	to, from := d.Env().To, d.Env().From
	if len(to) != 1 || to[0].MailboxName != "you" || to[0].HostName != "y.example" {
		t.Fatalf("cached To = %+v", to)
	}
	if len(from) != 1 || from[0].PersonalName != "Me" || from[0].MailboxName != "me" || from[0].HostName != "mine.example" {
		t.Fatalf("cached From = %+v", from)
	}
}
