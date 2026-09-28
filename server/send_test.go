package server

import (
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestIsSpecialMailbox(t *testing.T) {
	acc := &cache.Account{
		SentMailbox:    "Sent",
		DraftMailbox:   "Drafts",
		DeletedMailbox: "Junk",
	}
	tests := []struct {
		name string
		want bool
	}{
		{"", false},
		{"INBOX", true},
		{"inbox", true},
		{"Sent", true},
		{"sent", true},
		{"Drafts", true},
		{"Junk", true},
		{"cust.dassault", false},
		{"gitlab.hug", false},
	}
	for _, tt := range tests {
		if got := isSpecialMailbox(tt.name, acc); got != tt.want {
			t.Errorf("isSpecialMailbox(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}

	// Unconfigured special mailboxes must not match everything via empty string.
	empty := &cache.Account{}
	if isSpecialMailbox("cust.dassault", empty) {
		t.Error("isSpecialMailbox with unconfigured account matched a regular mailbox")
	}
	if !isSpecialMailbox("INBOX", empty) {
		t.Error("INBOX should always be special")
	}
}
