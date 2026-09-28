package cache

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// MemoryBackend keeps email content (envelope/body/headers/etc.) in memory only.
// A small per-account sidecar JSON file persists only the per-email state that
// must survive restarts to keep filter behavior correct: labels, plugins_run,
// and filtered. This prevents side-effecting plugins (e.g. slack-notify) from
// re-firing and already-swept emails from being re-evaluated on every restart,
// while still avoiding writing email bodies to disk.
type MemoryBackend struct {
	baseDir string
	mu      sync.Mutex
	// sidecars[account][mailbox][uid] -> persistedState
	sidecars map[string]map[string]map[uint32]persistedState
	// lastHash[account] -> sha256 of last-written sidecar bytes; skip identical rewrites.
	lastHash map[string][32]byte
}

// persistedState is the slice of CachedEmail we write to disk.
type persistedState struct {
	Labels     []string `json:"labels,omitempty"`
	PluginsRun []string `json:"plugins_run,omitempty"`
	Filtered   bool     `json:"filtered,omitempty"`
}

// NewMemoryBackend creates a memory-only backend. If baseDir is non-empty, it
// also loads/saves a small sidecar JSON per account containing labels,
// plugins_run, and filtered state for each email.
func NewMemoryBackend(baseDir string) *MemoryBackend {
	m := &MemoryBackend{
		baseDir:  baseDir,
		sidecars: make(map[string]map[string]map[uint32]persistedState),
		lastHash: make(map[string][32]byte),
	}
	return m
}

func (m *MemoryBackend) sidecarPath(accountName string) string {
	if m.baseDir == "" {
		return ""
	}
	return filepath.Join(m.baseDir, accountName, "memory-sidecar.json")
}

// loadSidecarLocked reads the sidecar file for an account into m.sidecars.
// Caller must hold m.mu.
func (m *MemoryBackend) loadSidecarLocked(accountName string) {
	if _, ok := m.sidecars[accountName]; ok {
		return
	}
	m.sidecars[accountName] = make(map[string]map[uint32]persistedState)
	path := m.sidecarPath(accountName)
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("memory backend: failed to read sidecar", "account", accountName, "path", path, "error", err)
		}
		return
	}
	var stored map[string]map[uint32]persistedState
	if err := json.Unmarshal(data, &stored); err != nil {
		slog.Warn("memory backend: failed to parse sidecar", "account", accountName, "error", err)
		return
	}
	m.sidecars[accountName] = stored
}

// saveSidecarLocked writes the in-memory sidecar map back to disk for an account.
// Caller must hold m.mu.
func (m *MemoryBackend) saveSidecarLocked(accountName string) {
	path := m.sidecarPath(accountName)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("memory backend: failed to mkdir for sidecar", "path", path, "error", err)
		return
	}
	data, err := json.MarshalIndent(m.sidecars[accountName], "", "  ")
	if err != nil {
		slog.Warn("memory backend: failed to marshal sidecar", "account", accountName, "error", err)
		return
	}
	h := sha256.Sum256(data)
	if prev, ok := m.lastHash[accountName]; ok && prev == h {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Warn("memory backend: failed to write sidecar", "path", path, "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		slog.Warn("memory backend: failed to rename sidecar", "path", path, "error", err)
		return
	}
	m.lastHash[accountName] = h
}

func (m *MemoryBackend) LoadMailboxes(accountName string) (map[string]*Mailbox, error) {
	// Email content is not persisted, so there are no mailboxes to load. The
	// IMAP sync layer will re-discover them on startup. We still ensure the
	// sidecar is loaded so subsequent SaveEmails can merge persisted state in.
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadSidecarLocked(accountName)
	return nil, nil
}

// LoadEmails always returns nil — email content lives in memory only.
// Persisted per-email state is merged into emails by ApplySidecar, called
// from the sync path after IMAP populates new CachedEmail entries.
func (m *MemoryBackend) LoadEmails(accountName, mailboxName string) (map[uint32]*CachedEmail, error) {
	return nil, nil
}

// ApplySidecar merges persisted labels/plugins_run/filtered into the given
// emails map. Called by the sync layer once IMAP has populated emails so that
// per-email state survives restarts even though bodies don't.
func (m *MemoryBackend) ApplySidecar(accountName, mailboxName string, emails map[uint32]*CachedEmail) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadSidecarLocked(accountName)
	mboxStates, ok := m.sidecars[accountName][mailboxName]
	if !ok {
		return
	}
	for uid, state := range mboxStates {
		e, ok := emails[uid]
		if !ok {
			continue
		}
		if len(state.Labels) > 0 {
			e.Labels = append([]string(nil), state.Labels...)
		}
		if len(state.PluginsRun) > 0 {
			e.PluginsRun = append([]string(nil), state.PluginsRun...)
		}
		if state.Filtered {
			e.Filtered = true
		}
	}
}

func (m *MemoryBackend) SaveMailboxes(accountName string, mailboxes map[string]*Mailbox) error {
	return nil
}

// SaveEmailsWithHint falls back to a full save; the sidecar has no per-row
// change tracking.
func (m *MemoryBackend) SaveEmailsWithHint(accountName, mailboxName string, emails map[uint32]*CachedEmail, changedUIDs []uint32) error {
	return m.SaveEmails(accountName, mailboxName, emails)
}

// SaveEmails persists only the per-email state that must survive restarts.
// Email content (body, headers, envelope) is intentionally not written.
func (m *MemoryBackend) SaveEmails(accountName, mailboxName string, emails map[uint32]*CachedEmail) error {
	if m.baseDir == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadSidecarLocked(accountName)
	// Rebuild the mailbox map from current emails, dropping entries for UIDs
	// no longer present so the file doesn't grow forever.
	fresh := make(map[uint32]persistedState, len(emails))
	for uid, e := range emails {
		st := persistedState{
			Labels:     append([]string(nil), e.Labels...),
			PluginsRun: append([]string(nil), e.PluginsRun...),
			Filtered:   e.Filtered,
		}
		// Skip entries that have no state worth saving.
		if len(st.Labels) == 0 && len(st.PluginsRun) == 0 && !st.Filtered {
			continue
		}
		fresh[uid] = st
	}
	if len(fresh) == 0 {
		delete(m.sidecars[accountName], mailboxName)
	} else {
		m.sidecars[accountName][mailboxName] = fresh
	}
	m.saveSidecarLocked(accountName)
	return nil
}

// ResetFiltered clears the filtered marks in the account's sidecar.
func (m *MemoryBackend) ResetFiltered(accountName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadSidecarLocked(accountName)
	for _, mboxStates := range m.sidecars[accountName] {
		for uid, st := range mboxStates {
			st.Filtered = false
			mboxStates[uid] = st
		}
	}
	m.saveSidecarLocked(accountName)
	return nil
}

func (m *MemoryBackend) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for account := range m.sidecars {
		m.saveSidecarLocked(account)
	}
	return nil
}

// Compile-time check.
var _ CacheBackend = (*MemoryBackend)(nil)

// String returns a short identifier for logging.
func (m *MemoryBackend) String() string {
	return fmt.Sprintf("memory(sidecar=%q)", m.baseDir)
}
