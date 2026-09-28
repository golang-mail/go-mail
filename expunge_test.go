package main

import (
	"testing"

	"github.com/golang-mail/go-mail/filter"
)

// delete only flags; permanent_delete is the action that expunges.
func TestMailboxesToExpunge_OnlyPermanentDelete(t *testing.T) {
	actions := []pendingAction{
		{mailbox: "INBOX", uid: 1, action: filter.Action{Type: "delete"}},
		{mailbox: "Junk", uid: 2, action: filter.Action{Type: "Permanent_Delete"}},
		{mailbox: "Lists", uid: 3, action: filter.Action{Type: "move", Value: "Archive"}},
	}
	got := mailboxesToExpunge(actions)
	if len(got) != 1 || !got["Junk"] {
		t.Fatalf("expunge set = %v, want only Junk", got)
	}
}
