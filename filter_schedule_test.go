package main

import (
	"os"
	"testing"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/filter"
)

const scheduledFiltersYAML = `filters:
  - name: on
    conditions:
      - {field: subject, operator: contains, value: a}
    actions:
      - {type: mark_as_read}
  - name: off
    disabled: true
    conditions:
      - {field: subject, operator: contains, value: b}
    actions:
      - {type: mark_as_read}
  - name: later
    active_from: "2099-01-01T00:00:00Z"
    conditions:
      - {field: subject, operator: contains, value: c}
    actions:
      - {type: mark_as_read}
`

func writeScheduledFilters(t *testing.T) {
	t.Helper()
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })
	if err := os.WriteFile(cache.ConfigPath("filters.yml"), []byte(scheduledFiltersYAML), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFiltersConfigSkipsInactive(t *testing.T) {
	writeScheduledFilters(t)
	cfg, err := loadFiltersConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Filters) != 1 || cfg.Filters[0].Name != "on" {
		t.Fatalf("run config = %+v, want only \"on\"", cfg.Filters)
	}
}

func TestRearrangeKeepsInactiveFilters(t *testing.T) {
	writeScheduledFilters(t)
	if err := rearrangeFilters(); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadAllFiltersConfig()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]filter.Filter{}
	for _, f := range cfg.Filters {
		byName[f.Name] = f
	}
	if len(byName) != 3 {
		t.Fatalf("rearranged filters = %+v, want all three", cfg.Filters)
	}
	if !byName["off"].Disabled || byName["later"].ActiveFrom == "" {
		t.Fatalf("rearrange must keep schedule fields: %+v", cfg.Filters)
	}
}
