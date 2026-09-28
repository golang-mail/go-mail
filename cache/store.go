package cache

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

func (s *Store) LoadCache() {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	for i := range s.Accounts {
		account := s.Accounts[i]
		mailboxes, err := s.Backend.LoadMailboxes(account.Name)
		if err != nil {
			slog.Error("failed to load mailboxes", "account", account.Name, "error", err)
			continue
		}
		if mailboxes == nil {
			mailboxes = make(map[string]*Mailbox)
		}
		account.Mailboxes = mailboxes

		totalEmails := 0
		for name, mailbox := range account.Mailboxes {
			emails, err := s.Backend.LoadEmails(account.Name, name)
			if err != nil {
				slog.Error("failed to load emails", "account", account.Name, "mailbox", name, "error", err)
				mailbox.Emails = make(map[uint32]*CachedEmail)
				continue
			}
			// Corrupt rows may lack an envelope; readers assume it is set.
			for _, e := range emails {
				if e.Envelope == nil {
					e.Envelope = &Envelope{}
				}
			}
			mailbox.Emails = emails
			totalEmails += len(emails)
		}
		slog.Info("loaded cache", "account", account.Name, "mailboxes", len(mailboxes), "emails", totalEmails)
	}
}

// Store provides thread-safe access to the accounts cache.
type Store struct {
	Mu              sync.RWMutex
	Accounts        []*Account
	OutgoingServers map[string]OutgoingServer
	Backend         CacheBackend
	SyncStatus      atomic.Value // stores string: "loading", "syncing", "filtering", "ready"

	// dirty tracks mailboxes whose emails changed since the last save, so
	// SaveCache rewrites only those. dirtyUIDs narrows it to single emails so
	// backends can skip the whole-mailbox change detection. Both are guarded
	// by dirtyMu, not Mu, so any mutation site can mark regardless of which
	// lock it holds.
	dirtyMu   sync.Mutex
	dirty     map[string]map[string]bool
	dirtyUIDs map[string]map[string]map[uint32]bool
}

// NewStore creates a new thread-safe store.
func NewStore() *Store {
	return &Store{
		Accounts:        []*Account{},
		OutgoingServers: make(map[string]OutgoingServer),
		dirty:           make(map[string]map[string]bool),
		dirtyUIDs:       make(map[string]map[string]map[uint32]bool),
	}
}

// MarkDirty records that account/mailbox has unsaved email changes.
func (s *Store) MarkDirty(account, mailbox string) {
	s.dirtyMu.Lock()
	defer s.dirtyMu.Unlock()
	if s.dirty == nil {
		s.dirty = make(map[string]map[string]bool)
	}
	if s.dirty[account] == nil {
		s.dirty[account] = make(map[string]bool)
	}
	s.dirty[account][mailbox] = true
	// A whole-mailbox dirty supersedes per-uid tracking.
	if s.dirtyUIDs[account] != nil {
		delete(s.dirtyUIDs[account], mailbox)
	}
}

// MarkEmailDirty records a single changed email, so the backend persists
// only that row instead of re-hashing the whole mailbox on save.
func (s *Store) MarkEmailDirty(account, mailbox string, uid uint32) {
	s.dirtyMu.Lock()
	defer s.dirtyMu.Unlock()
	if s.dirty != nil && s.dirty[account] != nil && s.dirty[account][mailbox] {
		return // mailbox already fully dirty
	}
	if s.dirtyUIDs == nil {
		s.dirtyUIDs = make(map[string]map[string]map[uint32]bool)
	}
	if s.dirtyUIDs[account] == nil {
		s.dirtyUIDs[account] = make(map[string]map[uint32]bool)
	}
	if s.dirtyUIDs[account][mailbox] == nil {
		s.dirtyUIDs[account][mailbox] = make(map[uint32]bool)
	}
	s.dirtyUIDs[account][mailbox][uid] = true
}

func (s *Store) takeDirty() (map[string]map[string]bool, map[string]map[string]map[uint32]bool) {
	s.dirtyMu.Lock()
	defer s.dirtyMu.Unlock()
	d := s.dirty
	s.dirty = make(map[string]map[string]bool)
	u := s.dirtyUIDs
	s.dirtyUIDs = make(map[string]map[string]map[uint32]bool)
	return d, u
}

func (s *Store) SetOutgoingServers(m map[string]OutgoingServer) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.OutgoingServers = m
}

// SaveCache persists mailbox metadata for every account and email rows only
// for mailboxes marked dirty since the last save. Mailboxes with only
// per-email marks go through the hinted save path.
func (s *Store) SaveCache() {
	dirty, dirtyUIDs := s.takeDirty()
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	for _, account := range s.Accounts {
		if err := s.Backend.SaveMailboxes(account.Name, account.Mailboxes); err != nil {
			slog.Error("failed to save mailboxes", "account", account.Name, "error", err)
			continue
		}
		for name := range dirty[account.Name] {
			mailbox, ok := account.Mailboxes[name]
			if !ok {
				continue // deleted meanwhile; nothing to write
			}
			if err := s.Backend.SaveEmails(account.Name, mailbox.Name, mailbox.Emails); err != nil {
				slog.Error("failed to save emails", "account", account.Name, "mailbox", mailbox.Name, "error", err)
				s.MarkDirty(account.Name, name) // retry next time
			}
		}
		for name, uids := range dirtyUIDs[account.Name] {
			if dirty[account.Name][name] {
				continue // fully saved above
			}
			mailbox, ok := account.Mailboxes[name]
			if !ok {
				continue
			}
			changed := make([]uint32, 0, len(uids))
			for uid := range uids {
				changed = append(changed, uid)
			}
			if err := s.Backend.SaveEmailsWithHint(account.Name, mailbox.Name, mailbox.Emails, changed); err != nil {
				slog.Error("failed to save emails", "account", account.Name, "mailbox", mailbox.Name, "error", err)
				for _, uid := range changed {
					s.MarkEmailDirty(account.Name, name, uid) // retry next time
				}
			}
		}
	}
}

// SaveCacheAll rewrites every mailbox; the safety net for a missed MarkDirty.
func (s *Store) SaveCacheAll() {
	s.takeDirty()
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	for _, account := range s.Accounts {
		if err := s.Backend.SaveMailboxes(account.Name, account.Mailboxes); err != nil {
			slog.Error("failed to save mailboxes", "account", account.Name, "error", err)
			continue
		}
		for _, mailbox := range account.Mailboxes {
			if err := s.Backend.SaveEmails(account.Name, mailbox.Name, mailbox.Emails); err != nil {
				slog.Error("failed to save emails", "account", account.Name, "mailbox", mailbox.Name, "error", err)
			}
		}
	}
}
