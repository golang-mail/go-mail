package cache

import (
	"errors"
	"testing"
)

func TestIMAPErrIs(t *testing.T) {
	if !imapErrIs(errors.New("Mailbox ALREADY EXISTS"), "already exists") {
		t.Fatal("case-insensitive match expected")
	}
	if imapErrIs(nil, "already exists") {
		t.Fatal("nil error never matches")
	}
	if imapErrIs(errors.New("NO permission"), "already exists") {
		t.Fatal("unrelated error must not match")
	}
}
