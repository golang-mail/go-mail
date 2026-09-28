package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

func useTempConfigDir(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })
}

func TestSaveSessions_PrivateModeAndNoTempLeftovers(t *testing.T) {
	useTempConfigDir(t)
	// A pre-existing world-readable file must end up private after a save.
	if err := os.WriteFile(sessionsFilePath(), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sessions = map[string]time.Time{"tok": time.Now().Add(time.Hour)}
	mu.Unlock()

	saveSessions()

	info, err := os.Stat(sessionsFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("sessions.json mode = %o, want 600", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(sessionsFilePath()))
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temp file left behind: %v", names)
	}
}

func TestLoadSessions_TightensPermissions(t *testing.T) {
	useTempConfigDir(t)
	if err := os.WriteFile(sessionsFilePath(), []byte(`{"tok":"2099-01-01T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	loadSessions()
	info, err := os.Stat(sessionsFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("sessions.json mode after load = %o, want 600", got)
	}
	mu.Lock()
	_, ok := sessions["tok"]
	mu.Unlock()
	if !ok {
		t.Fatal("session not loaded")
	}
}
