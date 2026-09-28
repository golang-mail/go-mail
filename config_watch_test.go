package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitFired(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: callback not fired", what)
	}
}

func expectQuiet(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s: callback fired unexpectedly", what)
	case <-time.After(watchDebounce * 3):
	}
}

// renameSave mimics editors and sed -i: write a temp file, rename over target.
func renameSave(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestWatchConfigFilesSurvivesRenameSaves(t *testing.T) {
	dir := t.TempDir()
	filtersPath := filepath.Join(dir, "filters.yml")
	pluginsPath := filepath.Join(dir, "plugins.yml")
	if err := os.WriteFile(filtersPath, []byte("filters: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pluginsPath, []byte("plugins: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	filters := make(chan struct{}, 16)
	plugins := make(chan struct{}, 16)
	w, err := watchConfigFiles(dir, func() { filters <- struct{}{} }, func() { plugins <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// In-place write, as before.
	if err := os.WriteFile(pluginsPath, []byte("plugins: [a]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFired(t, plugins, "in-place plugins write")

	// Rename-style save, then a later in-place write must still be seen.
	renameSave(t, pluginsPath, "plugins: [b]\n")
	waitFired(t, plugins, "rename-saved plugins.yml")
	if err := os.WriteFile(pluginsPath, []byte("plugins: [c]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFired(t, plugins, "plugins write after rename")

	renameSave(t, filtersPath, "filters: [x]\n")
	waitFired(t, filters, "rename-saved filters.yml")
	expectQuiet(t, plugins, "filters save must not reload plugins")

	// Unrelated files in the config dir are ignored.
	if err := os.WriteFile(filepath.Join(dir, "labels.yml"), []byte("labels: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectQuiet(t, filters, "labels.yml write")
	expectQuiet(t, plugins, "labels.yml write")
}

func TestWatchConfigFilesDebouncesBursts(t *testing.T) {
	dir := t.TempDir()
	filtersPath := filepath.Join(dir, "filters.yml")
	filters := make(chan struct{}, 16)
	w, err := watchConfigFiles(dir, func() { filters <- struct{}{} }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filtersPath, []byte("filters: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	waitFired(t, filters, "burst")
	expectQuiet(t, filters, "burst must coalesce into one run")
}
