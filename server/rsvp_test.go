package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

const sampleInvite = "Please join.\n\nBEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\n" +
	"UID:evt-123@example.com\r\nSEQUENCE:2\r\nDTSTART:20260910T090000Z\r\nDTEND:20260910T100000Z\r\n" +
	"SUMMARY:Planning\r\n  meeting\r\n" +
	"ORGANIZER;CN=Olive Organizer:mailto:olive@example.com\r\n" +
	"ATTENDEE;CN=Me;ROLE=REQ-PARTICIPANT:mailto:Me@Mine.example\r\n" +
	"ATTENDEE;CN=Other:mailto:other@example.com\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func TestParseInvite_ReadsEventFields(t *testing.T) {
	ev, err := parseInvite(sampleInvite)
	if err != nil {
		t.Fatal(err)
	}
	if ev.UID != "evt-123@example.com" || ev.Sequence != "2" {
		t.Fatalf("uid/sequence = %q/%q", ev.UID, ev.Sequence)
	}
	if ev.Organizer != "olive@example.com" || ev.OrganizerName != "Olive Organizer" {
		t.Fatalf("organizer = %q (%q)", ev.Organizer, ev.OrganizerName)
	}
	if ev.Summary != "Planning meeting" {
		t.Fatalf("folded summary = %q", ev.Summary)
	}
	if ev.DTStart.Value != "20260910T090000Z" || ev.DTEnd.Value != "20260910T100000Z" || ev.DTStart.Params != "" {
		t.Fatalf("dtstart/dtend = %+v/%+v", ev.DTStart, ev.DTEnd)
	}
	if len(ev.Attendees) != 2 || ev.Attendees[0] != "me@mine.example" || ev.Attendees[1] != "other@example.com" {
		t.Fatalf("attendees = %v", ev.Attendees)
	}
}

func TestParseInvite_Errors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"no vevent", "hello"},
		{"no organizer", "BEGIN:VEVENT\r\nUID:x\r\nEND:VEVENT\r\n"},
		{"no uid", "BEGIN:VEVENT\r\nORGANIZER:mailto:a@b\r\nEND:VEVENT\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseInvite(tt.body); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestBuildRSVPReply(t *testing.T) {
	ev, _ := parseInvite(sampleInvite)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	out := buildRSVPReply(ev, "me@mine.example", "Me Myself", "TENTATIVE", now)
	for _, want := range []string{
		"METHOD:REPLY",
		"UID:evt-123@example.com",
		"SEQUENCE:2",
		"DTSTAMP:20260903T120000Z",
		"ORGANIZER;CN=Olive Organizer:mailto:olive@example.com",
		"ATTENDEE;CN=Me Myself;PARTSTAT=TENTATIVE:mailto:me@mine.example",
		"DTSTART:20260910T090000Z",
		"SUMMARY:Planning meeting",
	} {
		if !strings.Contains(out, want+"\r\n") {
			t.Errorf("reply missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "other@example.com") {
		t.Error("reply must list only the replying attendee")
	}
}

func TestPickRSVPAttendee(t *testing.T) {
	acc := &cache.Account{Username: "login@mine.example", FromAddrs: []cache.FromAddress{
		{Name: "Alias", Email: "alias@mine.example"},
		{Name: "Me Myself", Email: "me@mine.example"},
	}}
	tests := []struct {
		name      string
		attendees []string
		acc       *cache.Account
		wantAddr  string
		wantName  string
	}{
		{"matches invited from address", []string{"other@example.com", "me@mine.example"}, acc, "me@mine.example", "Me Myself"},
		{"falls back to first from address", []string{"other@example.com"}, acc, "alias@mine.example", "Alias"},
		{"falls back to username", nil, &cache.Account{Username: "login@mine.example"}, "login@mine.example", ""},
		{"username that is not an address", nil, &cache.Account{Username: "login"}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, name := pickRSVPAttendee(tt.acc, tt.attendees)
			if addr != tt.wantAddr || name != tt.wantName {
				t.Fatalf("got %q/%q want %q/%q", addr, name, tt.wantAddr, tt.wantName)
			}
		})
	}
}

func inviteStore() *cache.Store {
	e := sampleFiltered()
	e.ContentType = "text/calendar"
	e.Body = sampleInvite
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: e})
	store.Accounts[0].FromAddrs = []cache.FromAddress{{Name: "Me Myself", Email: "me@mine.example"}}
	return store
}

func rsvpReq(method, query string) *http.Request {
	return httptest.NewRequest(method, "/api/mail/rsvp?"+query, nil)
}

func TestRSVPHandler_Validation(t *testing.T) {
	store := inviteStore()
	plain := sampleFiltered()
	plain.UID = 2
	store.Accounts[0].Mailboxes["INBOX"].Emails[2] = plain

	tests := []struct {
		name   string
		method string
		query  string
		want   int
	}{
		{"get", http.MethodGet, "account=acc&mailbox=INBOX&uid=1&response=ACCEPTED", http.StatusMethodNotAllowed},
		{"bad response", http.MethodPost, "account=acc&mailbox=INBOX&uid=1&response=MAYBE", http.StatusBadRequest},
		{"unknown mail", http.MethodPost, "account=acc&mailbox=INBOX&uid=9&response=ACCEPTED", http.StatusNotFound},
		{"not an invite", http.MethodPost, "account=acc&mailbox=INBOX&uid=2&response=ACCEPTED", http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			rsvpHandler(store)(w, rsvpReq(tt.method, tt.query))
			if w.Code != tt.want {
				t.Fatalf("want %d got %d: %s", tt.want, w.Code, w.Body.String())
			}
		})
	}
}

func TestRSVPHandler_SendsReplyToOrganizer(t *testing.T) {
	store := inviteStore()
	var gotFrom string
	var gotTo []string
	var gotMsg string
	old := rsvpSend
	rsvpSend = func(_ *cache.Store, from string, to []string, msg []byte) error {
		gotFrom, gotTo, gotMsg = from, to, string(msg)
		return nil
	}
	t.Cleanup(func() { rsvpSend = old })

	w := httptest.NewRecorder()
	rsvpHandler(store)(w, rsvpReq(http.MethodPost, "account=acc&mailbox=INBOX&uid=1&response=DECLINED"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d: %s", w.Code, w.Body.String())
	}
	if gotFrom != "me@mine.example" || len(gotTo) != 1 || gotTo[0] != "olive@example.com" {
		t.Fatalf("from/to = %q/%v", gotFrom, gotTo)
	}
	for _, want := range []string{
		"Subject: Declined: Planning meeting",
		"To: \"Olive Organizer\" <olive@example.com>",
		"From: \"Me Myself\" <me@mine.example>",
		"Content-Type: multipart/alternative;",
		"Content-Type: text/calendar; charset=UTF-8; method=REPLY",
		"PARTSTAT=DECLINED",
	} {
		if !strings.Contains(gotMsg, want) {
			t.Errorf("message missing %q:\n%s", want, gotMsg)
		}
	}
}

func TestRSVPHandler_SMTPFailureIs502(t *testing.T) {
	store := inviteStore()
	old := rsvpSend
	rsvpSend = func(*cache.Store, string, []string, []byte) error { return errFakeSMTP }
	t.Cleanup(func() { rsvpSend = old })

	w := httptest.NewRecorder()
	rsvpHandler(store)(w, rsvpReq(http.MethodPost, "account=acc&mailbox=INBOX&uid=1&response=ACCEPTED"))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502 got %d", w.Code)
	}
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

var errFakeSMTP = fakeErr("smtp down")
