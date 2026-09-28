package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

func TestParseInvite_GoogleFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/google-invite.ics")
	if err != nil {
		t.Fatal(err)
	}
	ev, err := parseInvite(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if ev.UID != "5k3j2h1g0f9e8d7c6b5a4@google.com" || ev.Sequence != "0" {
		t.Fatalf("uid/seq = %q/%q", ev.UID, ev.Sequence)
	}
	if ev.Summary != "Q3 planning, part 1" {
		t.Fatalf("summary = %q", ev.Summary)
	}
	if ev.DTStart.Value != "20260915T140000" || ev.DTStart.Params != "TZID=Europe/Zagreb" {
		t.Fatalf("dtstart = %+v", ev.DTStart)
	}
	if len(ev.Attendees) != 2 || ev.Attendees[1] != "me@mine.example" {
		t.Fatalf("attendees = %v (folded parameter line must survive)", ev.Attendees)
	}
}

func TestBuildRSVPReply_PreservesDateParamsAndRecurrenceID(t *testing.T) {
	body := "BEGIN:VEVENT\r\nUID:u1\r\nORGANIZER:mailto:o@x\r\n" +
		"DTSTART;VALUE=DATE:20260920\r\nDTEND;VALUE=DATE:20260921\r\n" +
		"RECURRENCE-ID;TZID=Europe/Zagreb:20260920T090000\r\n" +
		"END:VEVENT\r\n"
	ev, err := parseInvite(body)
	if err != nil {
		t.Fatal(err)
	}
	out := buildRSVPReply(ev, "me@mine.example", "", "ACCEPTED", time.Unix(0, 0).UTC())
	for _, want := range []string{
		"DTSTART;VALUE=DATE:20260920\r\n",
		"DTEND;VALUE=DATE:20260921\r\n",
		"RECURRENCE-ID;TZID=Europe/Zagreb:20260920T090000\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("reply missing %q:\n%s", want, out)
		}
	}
}

func TestRSVPHandler_SavesCopyToSentMailbox(t *testing.T) {
	store := inviteStore()
	store.Accounts[0].SentMailbox = "Sent"
	oldSend := rsvpSend
	rsvpSend = func(*cache.Store, string, []string, []byte) error { return nil }
	t.Cleanup(func() { rsvpSend = oldSend })

	var gotAcc, gotMailbox, gotSubject string
	oldSave := rsvpSaveCopy
	rsvpSaveCopy = func(_ *cache.Store, acc *cache.Account, mailbox string, msg []byte, env *cache.Envelope) {
		gotAcc, gotMailbox, gotSubject = acc.Name, mailbox, env.Subject
	}
	t.Cleanup(func() { rsvpSaveCopy = oldSave })

	w := httptest.NewRecorder()
	rsvpHandler(store)(w, rsvpReq(http.MethodPost, "account=acc&mailbox=INBOX&uid=1&response=ACCEPTED"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d: %s", w.Code, w.Body.String())
	}
	if gotAcc != "acc" || gotMailbox != "Sent" || gotSubject != "Accepted: Planning meeting" {
		t.Fatalf("copy saved to %q/%q subject %q", gotAcc, gotMailbox, gotSubject)
	}
}

func TestRSVPHandler_NoSentMailboxSkipsCopy(t *testing.T) {
	store := inviteStore()
	oldSend := rsvpSend
	rsvpSend = func(*cache.Store, string, []string, []byte) error { return nil }
	t.Cleanup(func() { rsvpSend = oldSend })
	called := false
	oldSave := rsvpSaveCopy
	rsvpSaveCopy = func(*cache.Store, *cache.Account, string, []byte, *cache.Envelope) { called = true }
	t.Cleanup(func() { rsvpSaveCopy = oldSave })

	w := httptest.NewRecorder()
	rsvpHandler(store)(w, rsvpReq(http.MethodPost, "account=acc&mailbox=INBOX&uid=1&response=ACCEPTED"))
	if w.Code != http.StatusOK || called {
		t.Fatalf("code %d, copy called %v", w.Code, called)
	}
}
