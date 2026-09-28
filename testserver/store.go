package testserver

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"gopkg.in/yaml.v3"
)

// saveDelay coalesces bursts of mutations into one write.
const saveDelay = 50 * time.Millisecond

// store holds every user and persists them to StorageDir as YAML.
type store struct {
	dir string

	mu    sync.RWMutex
	users map[string]*User

	// saveMu serializes writers; timerMu guards the debounce timer.
	saveMu  sync.Mutex
	timerMu sync.Mutex
	timer   *time.Timer
	closed  bool
}

func newStore(dir string) *store {
	return &store{dir: dir, users: make(map[string]*User)}
}

func (s *store) user(username string) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.users[username]
}

func (s *store) addUser(username, password string) *User {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.users[username]; u != nil {
		return u
	}
	u := newUser(s, username, password)
	s.users[username] = u
	return u
}

func (s *store) userList() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].username < out[j].username })
	return out
}

// routeRecipient picks the user for an SMTP recipient: exact address or
// local part match, else the only user when there is just one.
func (s *store) routeRecipient(addr string) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	local, _, _ := strings.Cut(addr, "@")
	for name, u := range s.users {
		if strings.EqualFold(name, addr) || strings.EqualFold(name, local) {
			return u
		}
	}
	if len(s.users) == 1 {
		for _, u := range s.users {
			return u
		}
	}
	return nil
}

// markDirty schedules a save; it never blocks the mutating command.
func (s *store) markDirty() {
	if s.dir == "" {
		return
	}
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	if s.closed || s.timer != nil {
		return
	}
	s.timer = time.AfterFunc(saveDelay, func() {
		s.timerMu.Lock()
		s.timer = nil
		s.timerMu.Unlock()
		if err := s.save(); err != nil {
			slog.Error("testserver: save failed", "error", err)
		}
	})
}

// flush writes pending state now and waits for it.
func (s *store) flush() error {
	if s.dir == "" {
		return nil
	}
	s.timerMu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.timerMu.Unlock()
	return s.save()
}

func (s *store) close() error {
	s.timerMu.Lock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.timerMu.Unlock()
	if s.dir == "" {
		return nil
	}
	return s.save()
}

// StoredMessage is the on-disk form of one message.
type StoredMessage struct {
	Uid   uint32    `yaml:"uid"`
	Date  time.Time `yaml:"date"`
	Size  uint32    `yaml:"size"`
	Flags []string  `yaml:"flags"`
	Body  []byte    `yaml:"body"`
}

// StoredMailbox is the on-disk form of one mailbox, also used to seed fixtures.
type StoredMailbox struct {
	Name        string           `yaml:"name"`
	Subscribed  bool             `yaml:"subscribed"`
	UidNext     uint32           `yaml:"uid_next"`
	UidValidity uint32           `yaml:"uid_validity"`
	Messages    []*StoredMessage `yaml:"messages"`
}

type storedUser struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func mailboxFileName(name string) string {
	return strings.ReplaceAll(name, "/", "_") + ".yaml"
}

func (mbox *Mailbox) snapshot() *StoredMailbox {
	mbox.mutex.Lock()
	defer mbox.mutex.Unlock()
	pm := &StoredMailbox{
		Name:        mbox.name,
		Subscribed:  mbox.subscribed,
		UidNext:     uint32(mbox.uidNext),
		UidValidity: mbox.uidValidity,
		Messages:    make([]*StoredMessage, 0, len(mbox.l)),
	}
	for _, msg := range mbox.l {
		flags := msg.flagStrings()
		sort.Strings(flags)
		pm.Messages = append(pm.Messages, &StoredMessage{
			Uid:   uint32(msg.uid),
			Date:  msg.t,
			Size:  uint32(len(msg.buf)),
			Flags: flags,
			Body:  msg.buf,
		})
	}
	return pm
}

// save snapshots every mailbox under its lock, then writes each file
// atomically and removes files for mailboxes that no longer exist.
func (s *store) save() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	type userSnapshot struct {
		user      storedUser
		mailboxes []*StoredMailbox
	}
	var snaps []userSnapshot
	for _, u := range s.userList() {
		snap := userSnapshot{user: storedUser{Username: u.username, Password: u.password}}
		u.mutex.Lock()
		for _, mbox := range u.mailboxes {
			snap.mailboxes = append(snap.mailboxes, mbox.snapshot())
		}
		u.mutex.Unlock()
		snaps = append(snaps, snap)
	}

	users := make([]storedUser, 0, len(snaps))
	for _, snap := range snaps {
		users = append(users, snap.user)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	if err := writeYAMLAtomic(filepath.Join(s.dir, "users.yaml"), users); err != nil {
		return err
	}

	for _, snap := range snaps {
		userDir := filepath.Join(s.dir, snap.user.Username)
		if err := os.MkdirAll(userDir, 0o755); err != nil {
			return err
		}
		keep := make(map[string]bool, len(snap.mailboxes))
		for _, pm := range snap.mailboxes {
			file := mailboxFileName(pm.Name)
			keep[file] = true
			if err := writeYAMLAtomic(filepath.Join(userDir, file), pm); err != nil {
				return err
			}
		}
		entries, err := os.ReadDir(userDir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") || keep[e.Name()] {
				continue
			}
			if err := os.Remove(filepath.Join(userDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeYAMLAtomic(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// load reads users.yaml and every per-user mailbox file and reports which
// mailboxes were restored, per user. Missing files are not an error; a
// corrupt file is.
func (s *store) load() (map[string]map[string]bool, error) {
	loaded := make(map[string]map[string]bool)
	if s.dir == "" {
		return loaded, nil
	}

	usersFile := filepath.Join(s.dir, "users.yaml")
	if data, err := os.ReadFile(usersFile); err == nil {
		var users []storedUser
		if err := yaml.Unmarshal(data, &users); err != nil {
			return nil, fmt.Errorf("parse %s: %w", usersFile, err)
		}
		for _, su := range users {
			s.addUser(su.Username, su.Password)
		}
	}

	for _, u := range s.userList() {
		userDir := filepath.Join(s.dir, u.username)
		entries, err := os.ReadDir(userDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			fpath := filepath.Join(userDir, entry.Name())
			data, err := os.ReadFile(fpath)
			if err != nil {
				return nil, err
			}
			var pm StoredMailbox
			if err := yaml.Unmarshal(data, &pm); err != nil {
				return nil, fmt.Errorf("parse %s: %w", fpath, err)
			}
			if pm.Name == "" {
				return nil, fmt.Errorf("%s: mailbox has no name", fpath)
			}
			u.mutex.Lock()
			u.loadMailboxLocked(&pm)
			u.mutex.Unlock()
			if loaded[u.username] == nil {
				loaded[u.username] = make(map[string]bool)
			}
			loaded[u.username][normalizeName(pm.Name)] = true
		}
	}
	return loaded, nil
}

func (u *User) loadMailboxLocked(pm *StoredMailbox) {
	name := normalizeName(pm.Name)
	mbox := u.ensureMailboxLocked(name)
	if pm.UidValidity != 0 {
		mbox.uidValidity = pm.UidValidity
		if pm.UidValidity > u.prevUidValidity {
			u.prevUidValidity = pm.UidValidity
		}
	}
	mbox.mutex.Lock()
	defer mbox.mutex.Unlock()
	mbox.subscribed = pm.Subscribed
	mbox.l = mbox.l[:0]
	var maxUID uint32
	for _, sm := range pm.Messages {
		flags := make([]imap.Flag, 0, len(sm.Flags))
		for _, f := range sm.Flags {
			flags = append(flags, imap.Flag(f))
		}
		uid := sm.Uid
		if uid == 0 {
			uid = maxUID + 1
		}
		if uid > maxUID {
			maxUID = uid
		}
		mbox.l = append(mbox.l, newMessage(imap.UID(uid), sm.Body, sm.Date, flags))
	}
	mbox.uidNext = imap.UID(max(pm.UidNext, maxUID+1))
	mbox.tracker = imapserver.NewMailboxTracker(uint32(len(mbox.l)))
}
