package server

import "testing"

func TestUIDSeq(t *testing.T) {
	if got := uidSeq(4, 1, 2).String(); got != "1:2,4" {
		t.Fatalf("uidSeq = %q", got)
	}
	if !uidSeq().Empty() {
		t.Fatal("empty uidSeq must be empty")
	}
}
