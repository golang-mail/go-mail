package main

import (
	"reflect"
	"sort"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestRequiredMailboxes(t *testing.T) {
	tests := []struct {
		name string
		acc  *cache.Account
		want []string
	}{
		{"all configured", &cache.Account{SentMailbox: "Sent", DraftMailbox: "Drafts", DeletedMailbox: "Trash"}, []string{"Drafts", "Sent", "Trash"}},
		{"drafts only", &cache.Account{DraftMailbox: "Drafts"}, []string{"Drafts"}},
		{"duplicates collapse", &cache.Account{SentMailbox: "Archive", DeletedMailbox: "Archive"}, []string{"Archive"}},
		{"nothing configured", &cache.Account{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := requiredMailboxes(tt.acc)
			sort.Strings(got)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
