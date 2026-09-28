package cache

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v2"
)

// YAMLBackend persists cache data as YAML files on disk.
type YAMLBackend struct {
	baseDir string
	mu      sync.Mutex
	// lastHash[path] -> sha256 of last-written content; lets us skip identical rewrites.
	lastHash map[string][32]byte
}

// NewYAMLBackend creates a new YAML-based cache backend.
func NewYAMLBackend(baseDir string) *YAMLBackend {
	return &YAMLBackend{
		baseDir:  baseDir,
		lastHash: make(map[string][32]byte),
	}
}

func (b *YAMLBackend) LoadMailboxes(accountName string) (map[string]*Mailbox, error) {
	accountDir := fmt.Sprintf("%s/%s", b.baseDir, accountName)
	data, err := os.ReadFile(fmt.Sprintf("%s/db.mailboxes.yaml", accountDir))
	if err != nil {
		return nil, err
	}
	var mailboxes map[string]*Mailbox
	if err := yaml.Unmarshal(data, &mailboxes); err != nil {
		return nil, err
	}
	return mailboxes, nil
}

func (b *YAMLBackend) LoadEmails(accountName, mailboxName string) (map[uint32]*CachedEmail, error) {
	accountDir := fmt.Sprintf("%s/%s", b.baseDir, accountName)
	safeMailboxName := strings.ReplaceAll(mailboxName, "/", "_")
	data, err := os.ReadFile(fmt.Sprintf("%s/%s.mails.yaml", accountDir, safeMailboxName))
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[uint32]*CachedEmail), nil
		}
		return nil, err
	}
	var emails map[uint32]*CachedEmail
	if err := yaml.Unmarshal(data, &emails); err != nil {
		return nil, err
	}
	return emails, nil
}

// writeIfChanged writes data to path only if its hash differs from the last write.
func (b *YAMLBackend) writeIfChanged(path string, data []byte) error {
	h := sha256.Sum256(data)
	b.mu.Lock()
	prev, ok := b.lastHash[path]
	b.mu.Unlock()
	if ok && prev == h {
		return nil
	}
	if err := WriteFileAtomic(path, data, 0o644); err != nil {
		return err
	}
	b.mu.Lock()
	b.lastHash[path] = h
	b.mu.Unlock()
	return nil
}

func (b *YAMLBackend) SaveMailboxes(accountName string, mailboxes map[string]*Mailbox) error {
	accountDir := fmt.Sprintf("%s/%s", b.baseDir, accountName)
	if err := os.MkdirAll(accountDir, 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(mailboxes)
	if err != nil {
		return err
	}
	return b.writeIfChanged(fmt.Sprintf("%s/db.mailboxes.yaml", accountDir), data)
}

// SaveEmailsWithHint falls back to a full save; the yaml file is written whole.
func (b *YAMLBackend) SaveEmailsWithHint(accountName, mailboxName string, emails map[uint32]*CachedEmail, changedUIDs []uint32) error {
	return b.SaveEmails(accountName, mailboxName, emails)
}

func (b *YAMLBackend) SaveEmails(accountName, mailboxName string, emails map[uint32]*CachedEmail) error {
	accountDir := fmt.Sprintf("%s/%s", b.baseDir, accountName)
	if err := os.MkdirAll(accountDir, 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(emails)
	if err != nil {
		return err
	}
	safeMailboxName := strings.ReplaceAll(mailboxName, "/", "_")
	return b.writeIfChanged(fmt.Sprintf("%s/%s.mails.yaml", accountDir, safeMailboxName), data)
}

// ResetFiltered rewrites each mailbox file of the account with Filtered cleared.
func (b *YAMLBackend) ResetFiltered(accountName string) error {
	accountDir := fmt.Sprintf("%s/%s", b.baseDir, accountName)
	entries, err := os.ReadDir(accountDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".mails.yaml") {
			continue
		}
		path := fmt.Sprintf("%s/%s", accountDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var emails map[uint32]*CachedEmail
		if err := yaml.Unmarshal(data, &emails); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		changed := false
		for _, e := range emails {
			if e.Filtered {
				e.Filtered = false
				changed = true
			}
		}
		if !changed {
			continue
		}
		out, err := yaml.Marshal(emails)
		if err != nil {
			return err
		}
		if err := b.writeIfChanged(path, out); err != nil {
			return err
		}
	}
	return nil
}

func (b *YAMLBackend) Close() error {
	return nil
}
