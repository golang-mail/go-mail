package main

import "testing"

func TestResolveServerMailbox(t *testing.T) {
	existing := map[string]string{"inbox": "INBOX", "inbox/drafts": "INBOX/Drafts", "sent": "Sent"}
	tests := []struct {
		name       string
		want       string
		wantExists bool
	}{
		{"Sent", "Sent", true},
		{"sent", "Sent", true},
		{"Drafts", "INBOX/Drafts", true},
		{"INBOX/Drafts", "INBOX/Drafts", true},
		{"Trash", "Trash", false},
	}
	for _, tt := range tests {
		got, exists := resolveServerMailbox(existing, tt.name)
		if got != tt.want || exists != tt.wantExists {
			t.Errorf("resolveServerMailbox(%q) = %q,%v want %q,%v", tt.name, got, exists, tt.want, tt.wantExists)
		}
	}
}
