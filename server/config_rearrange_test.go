package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFiltersRearrange_AcceptsBothSpellings(t *testing.T) {
	for _, key := range []string{"rearange", "rearrange"} {
		t.Run(key, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "server.yml")
			if err := os.WriteFile(p, []byte("username: u\npassword: p\nport: 1\nfilters:\n  "+key+": true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadServerConfig(p)
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.Filters.Rearranged() {
				t.Fatalf("%s: true must enable rearranging", key)
			}
		})
	}
	p := filepath.Join(t.TempDir(), "server.yml")
	os.WriteFile(p, []byte("username: u\npassword: p\nport: 1\n"), 0o600)
	cfg, _ := LoadServerConfig(p)
	if cfg.Filters.Rearranged() {
		t.Fatal("default must be off")
	}
}
