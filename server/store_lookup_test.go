package server

import (
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestFindHelpers(t *testing.T) {
	e := sampleFiltered()
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: e})

	if findAccount(store, "nope") != nil {
		t.Fatal("unknown account must be nil")
	}
	if acc := findAccount(store, "acc"); acc == nil || acc.Name != "acc" {
		t.Fatal("known account not found")
	}

	if acc, mb := findMailbox(store, "acc", "Junk"); acc == nil || mb != nil {
		t.Fatalf("unknown mailbox: acc=%v mb=%v", acc, mb)
	}
	if acc, mb := findMailbox(store, "nope", "INBOX"); acc != nil || mb != nil {
		t.Fatal("unknown account must yield nil account and mailbox")
	}
	if _, mb := findMailbox(store, "acc", "INBOX"); mb == nil || mb.Name != "INBOX" {
		t.Fatal("known mailbox not found")
	}

	if acc, mb, got := findEmail(store, "acc", "INBOX", 1); acc == nil || mb == nil || got != e {
		t.Fatal("known email not found")
	}
	if acc, mb, got := findEmail(store, "acc", "INBOX", 2); acc == nil || mb == nil || got != nil {
		t.Fatal("unknown uid must yield nil email but keep account and mailbox")
	}
	if acc, mb, got := findEmail(store, "acc", "Junk", 1); acc == nil || mb != nil || got != nil {
		t.Fatal("unknown mailbox must yield nil mailbox and email")
	}
}
