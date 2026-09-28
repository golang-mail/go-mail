package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

func TestSweepFilterSchedules(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute).Format(time.RFC3339)
	future := now.Add(time.Hour).Format(time.RFC3339)
	src := "filters:\n" +
		"  - name: keep\n    conditions: []\n    actions: []\n" +
		"  - name: gone\n    active_until: " + past + "\n    on_expire: remove\n    conditions: []\n    actions: []\n" +
		"  - name: paused\n    active_until: " + past + "\n    conditions: []\n    actions: []\n" +
		"  - name: later\n    active_from: " + future + "\n    conditions: []\n    actions: []\n"
	if err := os.WriteFile(cache.ConfigPath("filters.yml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	changes, err := SweepFilterSchedules(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %v, want 2", changes)
	}
	cfg, err := readFiltersFile()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(cfg.Filters))
	for _, f := range cfg.Filters {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "keep,paused,later" {
		t.Fatalf("filters after sweep = %v", names)
	}
	if !cfg.Filters[1].Disabled || cfg.Filters[1].ActiveUntil != "" {
		t.Fatalf("paused filter should be disabled with schedule cleared: %+v", cfg.Filters[1])
	}
	if cfg.Filters[2].ActiveFrom != future {
		t.Fatalf("future schedule must survive the sweep: %+v", cfg.Filters[2])
	}
}

func TestSweepFilterSchedules_NoChangeLeavesFileUntouched(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })
	src := "filters:\n  - name: keep\n    conditions: []\n    actions: []\n"
	if err := os.WriteFile(cache.ConfigPath("filters.yml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	changes, err := SweepFilterSchedules(time.Now())
	if err != nil || changes != nil {
		t.Fatalf("changes = %v, err = %v; want none", changes, err)
	}
	got, _ := os.ReadFile(cache.ConfigPath("filters.yml"))
	if string(got) != src {
		t.Fatalf("file rewritten without changes:\n%s", got)
	}
}
