package main

import (
	"path/filepath"
	"testing"
)

func TestFullRunNeeded(t *testing.T) {
	state := filepath.Join(t.TempDir(), "db", "filters_last_full_run")
	a := []byte("filters:\n- name: a\n")
	b := []byte("filters:\n- name: b\n")

	if !fullRunNeeded(state, a) {
		t.Fatal("no recorded run: a full run is needed")
	}
	if err := recordFullRun(state, a); err != nil {
		t.Fatal(err)
	}
	if fullRunNeeded(state, a) {
		t.Fatal("same filters as the last full run: no full run needed")
	}
	if !fullRunNeeded(state, b) {
		t.Fatal("changed filters: a full run is needed")
	}
}
