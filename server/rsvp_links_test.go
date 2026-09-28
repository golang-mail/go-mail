package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

const googleInviteRaw = "From: Olive Organizer <olive@example.com>\r\n" +
	"To: me@mine.example\r\n" +
	"Subject: Invitation: Planning\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"outer\"\r\n\r\n" +
	"--outer\r\n" +
	"Content-Type: multipart/alternative; boundary=\"inner\"\r\n\r\n" +
	"--inner\r\n" +
	"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
	"Please join.\r\n" +
	"--inner\r\n" +
	"Content-Type: text/html; charset=UTF-8\r\n\r\n" +
	"<p><a href=\"https://calendar.google.com/calendar/event?action=VIEW&amp;eid=E1&amp;tok=T1\">View</a>\r\n" +
	"<a href=\"https://calendar.google.com/calendar/event?action=RESPOND&amp;eid=E1&amp;rst=1&amp;tok=T1&amp;ctz=Europe%2FZagreb\">Yes</a>\r\n" +
	"<a href=\"https://calendar.google.com/calendar/event?action=RESPOND&amp;eid=E1&amp;rst=2&amp;tok=T1&amp;ctz=Europe%2FZagreb\">No</a>\r\n" +
	"<a href=\"https://calendar.google.com/calendar/event?action=RESPOND&amp;eid=E1&amp;rst=3&amp;tok=T1&amp;ctz=Europe%2FZagreb\">Maybe</a>\r\n" +
	"<a href=\"https://evil.example/calendar/event?action=RESPOND&amp;rst=1&amp;tok=X\">fake</a></p>\r\n" +
	"--inner\r\n" +
	"Content-Type: text/calendar; charset=UTF-8; method=REQUEST\r\n\r\n" +
	"BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n" +
	"--inner--\r\n" +
	"--outer--\r\n"

func TestGoogleRSVPLinks(t *testing.T) {
	links := googleRSVPLinks([]byte(googleInviteRaw))
	want := map[string]string{
		"ACCEPTED":  "https://calendar.google.com/calendar/event?action=RESPOND&eid=E1&rst=1&tok=T1&ctz=Europe%2FZagreb",
		"DECLINED":  "https://calendar.google.com/calendar/event?action=RESPOND&eid=E1&rst=2&tok=T1&ctz=Europe%2FZagreb",
		"TENTATIVE": "https://calendar.google.com/calendar/event?action=RESPOND&eid=E1&rst=3&tok=T1&ctz=Europe%2FZagreb",
	}
	if len(links) != len(want) {
		t.Fatalf("got %d links %v, want %d", len(links), links, len(want))
	}
	for k, v := range want {
		if links[k] != v {
			t.Errorf("%s = %q, want %q", k, links[k], v)
		}
	}
}

func TestGoogleRSVPLinks_NoHTML(t *testing.T) {
	raw := "Content-Type: text/plain\r\n\r\nhttps://calendar.google.com/calendar/event?action=RESPOND&rst=1&tok=T\r\n"
	if links := googleRSVPLinks([]byte(raw)); len(links) != 0 {
		t.Fatalf("plain text must not yield links, got %v", links)
	}
}

func rsvpLinksReq(method, query string) *http.Request {
	return httptest.NewRequest(method, "/api/mail/rsvp-links?"+query, nil)
}

func TestRSVPLinksHandler_ReturnsGoogleLinks(t *testing.T) {
	store := inviteStore()
	old := rsvpFetchRaw
	var gotMailbox string
	var gotUID uint32
	rsvpFetchRaw = func(_ *cache.Account, mailbox string, uid uint32) ([]byte, error) {
		gotMailbox, gotUID = mailbox, uid
		return []byte(googleInviteRaw), nil
	}
	t.Cleanup(func() { rsvpFetchRaw = old })

	w := httptest.NewRecorder()
	rsvpLinksHandler(store)(w, rsvpLinksReq(http.MethodGet, "account=acc&mailbox=INBOX&uid=1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d: %s", w.Code, w.Body.String())
	}
	if gotMailbox != "INBOX" || gotUID != 1 {
		t.Fatalf("fetched %s/%d", gotMailbox, gotUID)
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got["DECLINED"], "rst=2") || len(got) != 3 {
		t.Fatalf("links = %v", got)
	}
}

func TestRSVPLinksHandler_Errors(t *testing.T) {
	store := inviteStore()
	plain := sampleFiltered()
	plain.UID = 2
	store.Accounts[0].Mailboxes["INBOX"].Emails[2] = plain
	old := rsvpFetchRaw
	rsvpFetchRaw = func(*cache.Account, string, uint32) ([]byte, error) { return nil, errors.New("imap down") }
	t.Cleanup(func() { rsvpFetchRaw = old })

	tests := []struct {
		name   string
		method string
		query  string
		want   int
	}{
		{"post", http.MethodPost, "account=acc&mailbox=INBOX&uid=1", http.StatusMethodNotAllowed},
		{"bad uid", http.MethodGet, "account=acc&mailbox=INBOX&uid=x", http.StatusBadRequest},
		{"unknown mail", http.MethodGet, "account=acc&mailbox=INBOX&uid=9", http.StatusNotFound},
		{"not an invite", http.MethodGet, "account=acc&mailbox=INBOX&uid=2", http.StatusUnprocessableEntity},
		{"imap failure", http.MethodGet, "account=acc&mailbox=INBOX&uid=1", http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			rsvpLinksHandler(store)(w, rsvpLinksReq(tt.method, tt.query))
			if w.Code != tt.want {
				t.Fatalf("want %d got %d: %s", tt.want, w.Code, w.Body.String())
			}
		})
	}
}
