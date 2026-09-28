package main

import (
	"reflect"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestIdleMailboxes(t *testing.T) {
	tests := []struct {
		name string
		acc  *cache.Account
		want []string
	}{
		{"default INBOX", &cache.Account{}, []string{"INBOX"}},
		{"configured list", &cache.Account{IdleMailboxes: []string{"INBOX", "Lists"}}, []string{"INBOX", "Lists"}},
		{"duplicates and blanks dropped", &cache.Account{IdleMailboxes: []string{"INBOX", "", "INBOX"}}, []string{"INBOX"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := idleMailboxes(tt.acc); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
