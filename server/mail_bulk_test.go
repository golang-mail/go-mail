package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func post(h http.HandlerFunc, target, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)))
	return w
}

func postJSON(h http.HandlerFunc, target string, body any) *httptest.ResponseRecorder {
	data, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	h(w, r)
	return w
}

// The seen/unseen handlers must report IMAP failure instead of returning 200
// and silently diverging from the server.
func TestMarkAsSeenReportsIMAPFailure(t *testing.T) {
	e := sampleFiltered() // uid 1, flags nil (unseen)
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: e})
	// The account points nowhere: the IMAP store must fail.
	w := post(markAsSeenHandler(store), "/api/mail/seen?account=acc&mailbox=INBOX&uid=1", "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d: %s", w.Code, w.Body.String())
	}
	store.Mu.RLock()
	flags := store.Accounts[0].Mailboxes["INBOX"].Emails[1].Flags
	store.Mu.RUnlock()
	if len(flags) != 0 {
		t.Fatalf("cache must stay untouched on IMAP failure, flags = %v", flags)
	}
}

func TestMarkAsSeenUpdatesServerThenCache(t *testing.T) {
	e := sampleFiltered()
	ts, store := startServerBackedStore(t, map[uint32]*cache.CachedEmail{1: e})
	uid, err := ts.Deliver("u", "INBOX", []byte("From: a@x\r\nTo: u@x\r\nSubject: s\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	// Make the cached uid match the delivered one.
	store.Mu.Lock()
	mbox := store.Accounts[0].Mailboxes["INBOX"]
	delete(mbox.Emails, 1)
	e.UID = uid
	mbox.Emails[uid] = e
	store.Mu.Unlock()

	w := post(markAsSeenHandler(store), "/api/mail/seen?account=acc&mailbox=INBOX&uid="+strconv.Itoa(int(uid)), "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	acc := store.Accounts[0]
	if flags := serverFlags(t, acc, "INBOX", uid); !hasFlag(flags, `\Seen`) {
		t.Fatalf("server flags = %v, want \\Seen", flags)
	}
	store.Mu.RLock()
	cached := store.Accounts[0].Mailboxes["INBOX"].Emails[uid]
	store.Mu.RUnlock()
	if !hasFlag(cached.Flags, `\Seen`) {
		t.Fatalf("cached flags = %v, want \\Seen", cached.Flags)
	}
}

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

// Bulk delete/move with all:true must spare mail that has never been shown
// (Filtered==false); explicit UID lists keep operating on exactly those.
func TestDeleteAllSparesUnfilteredMail(t *testing.T) {
	filtered := sampleFiltered() // uid 1, Filtered true
	unfiltered := sampleFiltered()
	unfiltered.UID = 2
	unfiltered.Filtered = false
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: filtered, 2: unfiltered})

	w := postJSON(deleteAllHandler(store), "/api/mails/delete-selected",
		map[string]any{"account": "acc", "mailbox": "INBOX", "all": true})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	store.Mu.RLock()
	mbox := store.Accounts[0].Mailboxes["INBOX"]
	_, kept := mbox.Emails[2]
	_, gone := mbox.Emails[1]
	store.Mu.RUnlock()
	if gone {
		t.Fatal("filtered mail must be deleted")
	}
	if !kept {
		t.Fatal("never-shown mail must be spared by all:true")
	}

	// Explicit uids still delete unfiltered mail.
	w = postJSON(deleteAllHandler(store), "/api/mails/delete-selected",
		map[string]any{"account": "acc", "mailbox": "INBOX", "uids": []uint32{2}})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	store.Mu.RLock()
	_, kept = store.Accounts[0].Mailboxes["INBOX"].Emails[2]
	store.Mu.RUnlock()
	if kept {
		t.Fatal("explicit uid delete must still delete unfiltered mail")
	}
}

func TestMoveAllSparesUnfilteredMail(t *testing.T) {
	filtered := sampleFiltered()
	filtered.UID = 1
	unfiltered := sampleFiltered()
	unfiltered.UID = 2
	unfiltered.Filtered = false
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: filtered, 2: unfiltered})

	w := postJSON(moveSelectedHandler(store), "/api/mails/move-selected",
		map[string]any{"account": "acc", "mailbox": "INBOX", "to_mailbox": "Archive", "all": true})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	store.Mu.RLock()
	archived := store.Accounts[0].Mailboxes["Archive"]
	_, stillInInbox := store.Accounts[0].Mailboxes["INBOX"].Emails[2]
	store.Mu.RUnlock()
	if archived == nil || archived.Emails[1] == nil {
		t.Fatal("filtered mail must move")
	}
	if !stillInInbox {
		t.Fatal("never-shown mail must be spared by all:true")
	}
}

func TestJSONBodyCapped(t *testing.T) {
	store := newHandlerStore(nil)
	big := `{"uids":[` + strings.Repeat("1,", maxJSONBody/2) + "1],\"all\":true}"
	w := postJSON(deleteAllHandler(store), "/api/mails/delete-selected", json.RawMessage(big))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: want 400, got %d", w.Code)
	}
}
