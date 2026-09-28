package server

import (
	"bytes"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/emersion/go-imap"

	"github.com/golang-mail/go-mail/cache"
)

func newHandlerStore(emails map[uint32]*cache.CachedEmail) *cache.Store {
	store := cache.NewStore()
	store.Accounts = []*cache.Account{{
		Name: "acc",
		Mailboxes: map[string]*cache.Mailbox{
			"INBOX": {Name: "INBOX", Messages: uint32(len(emails)), Unseen: uint32(len(emails)), Emails: emails},
		},
	}}
	return store
}

func get(h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

// --- H4 pagination ---

func TestClampPage(t *testing.T) {
	tests := []struct {
		name               string
		page, size, total  int
		wantStart, wantEnd int
	}{
		{"first page", 1, 100, 250, 0, 100},
		{"last partial page", 3, 100, 250, 200, 250},
		{"page past end", 9, 100, 250, 250, 250},
		{"huge page does not overflow", math.MaxInt, 100, 10, 10, 10},
		{"huge size does not overflow", 2, math.MaxInt, 10, 10, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := clampPage(tt.page, tt.size, tt.total)
			if start != tt.wantStart || end != tt.wantEnd {
				t.Errorf("clampPage(%d,%d,%d) = %d,%d want %d,%d", tt.page, tt.size, tt.total, start, end, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestMailsHandler_HugePageIsNot500(t *testing.T) {
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: sampleFiltered()})
	w := get(mailsHandler(store), "/api/mails?account=acc&mailbox=INBOX&page=9223372036854775807&pageSize=9223372036854775807")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), `"pageSize":9223372036854775807`) {
		t.Fatal("pageSize was not capped")
	}
}

// --- H2 message count underflow ---

func TestRemoveCachedEmail(t *testing.T) {
	e := sampleFiltered()
	mbox := &cache.Mailbox{Messages: 1, Unseen: 1, Emails: map[uint32]*cache.CachedEmail{1: e}}

	if _, _, ok := removeCachedEmail(mbox, 2); ok {
		t.Fatal("removing a missing uid reported ok")
	}
	if mbox.Messages != 1 || mbox.Unseen != 1 {
		t.Fatalf("counters changed for missing uid: messages=%d unseen=%d", mbox.Messages, mbox.Unseen)
	}

	got, unseen, ok := removeCachedEmail(mbox, 1)
	if !ok || got != e || !unseen {
		t.Fatalf("remove uid 1: ok=%v email=%p unseen=%v", ok, got, unseen)
	}
	if mbox.Messages != 0 || mbox.Unseen != 0 || len(mbox.Emails) != 0 {
		t.Fatalf("counters not decremented: messages=%d unseen=%d len=%d", mbox.Messages, mbox.Unseen, len(mbox.Emails))
	}
}

func TestDeleteHandler_MissingUIDLeavesCountAlone(t *testing.T) {
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: sampleFiltered()})
	w := httptest.NewRecorder()
	deleteHandler(store)(w, httptest.NewRequest(http.MethodPost, "/api/mail/delete?account=acc&mailbox=INBOX&uid=42", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
	if got := store.Accounts[0].Mailboxes["INBOX"].Messages; got != 1 {
		t.Fatalf("message count changed to %d", got)
	}
}

// --- H1 attachment fetch ---

type literal struct{ *bytes.Buffer }

func (l literal) Len() int { return l.Buffer.Len() }

func imapMessage(raw string) *imap.Message {
	section, _ := imap.ParseBodySectionName("BODY[]")
	return &imap.Message{Body: map[*imap.BodySectionName]imap.Literal{section: literal{bytes.NewBufferString(raw)}}}
}

const singlePartMail = "From: a@b.c\r\nSubject: hi\r\nContent-Type: text/plain\r\n\r\nhello\r\n"

const nestedMail = "From: a@b.c\r\nContent-Type: multipart/mixed; boundary=outer\r\n\r\n" +
	"--outer\r\nContent-Type: multipart/alternative; boundary=inner\r\n\r\n" +
	"--inner\r\nContent-Type: text/plain\r\n\r\nbody\r\n--inner--\r\n" +
	"--outer\r\nContent-Type: text/plain; name=\"notes.txt\"\r\nContent-Disposition: attachment; filename=\"notes.txt\"\r\n\r\nATTACHED\r\n" +
	"--outer--\r\n"

func TestAttachmentFromMessage(t *testing.T) {
	if _, err := attachmentFromMessage(nil, "x"); err == nil {
		t.Fatal("nil message must return an error, not panic")
	}
	if _, err := attachmentFromMessage(&imap.Message{}, "x"); err == nil {
		t.Fatal("message without body must return an error")
	}
	if _, err := attachmentFromMessage(imapMessage(singlePartMail), "x"); err == nil {
		t.Fatal("single-part message must return not-found, not panic")
	}
	part, err := attachmentFromMessage(imapMessage(nestedMail), "notes.txt")
	if err != nil {
		t.Fatalf("nested lookup: %v", err)
	}
	data, _ := io.ReadAll(part.Body)
	if strings.TrimSpace(string(data)) != "ATTACHED" {
		t.Fatalf("wrong part body: %q", data)
	}
}

// --- H3 nil envelopes ---

func sampleFiltered() *cache.CachedEmail {
	e := sampleEmail()
	e.UID = 1
	e.Filtered = true
	e.Flags = nil
	return e
}

func TestReadHandlersTolerateNilEnvelope(t *testing.T) {
	e := sampleFiltered()
	e.Envelope = nil
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: e})
	var ran atomic.Bool
	ran.Store(true)

	tests := []struct {
		name string
		h    http.HandlerFunc
		url  string
	}{
		{"mails", mailsHandler(store), "/api/mails?account=acc&mailbox=INBOX"},
		{"mail", mailHandler(store), "/api/mail?account=acc&mailbox=INBOX&uid=1"},
		{"search", searchHandler(store), "/api/search?account=acc&query=anything"},
		{"unseen-summary", unseenSummaryHandler(store, &ran), "/api/unseen-summary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := get(tt.h, tt.url); w.Code != http.StatusOK {
				t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
			}
		})
	}
}
