package testserver

import (
	"crypto/subtle"
	"sort"
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// User owns a set of mailboxes. INBOX is matched case-insensitively as RFC
// 3501 requires; every other name is exact.
type User struct {
	username, password string
	store              *store

	mutex           sync.Mutex
	mailboxes       map[string]*Mailbox
	prevUidValidity uint32
}

func newUser(st *store, username, password string) *User {
	return &User{
		username:  username,
		password:  password,
		store:     st,
		mailboxes: make(map[string]*Mailbox),
	}
}

func normalizeName(name string) string {
	name = strings.TrimRight(name, string(mailboxDelim))
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	return name
}

func (u *User) login(username, password string) error {
	if username != u.username {
		return imapserver.ErrAuthFailed
	}
	if subtle.ConstantTimeCompare([]byte(password), []byte(u.password)) != 1 {
		return imapserver.ErrAuthFailed
	}
	return nil
}

func errNoSuchMailbox() error {
	return &imap.Error{
		Type: imap.StatusResponseTypeNo,
		Code: imap.ResponseCodeNonExistent,
		Text: "No such mailbox",
	}
}

func errTryCreate() error {
	return &imap.Error{
		Type: imap.StatusResponseTypeNo,
		Code: imap.ResponseCodeTryCreate,
		Text: "No such mailbox",
	}
}

func (u *User) mailboxLocked(name string) (*Mailbox, error) {
	mbox := u.mailboxes[normalizeName(name)]
	if mbox == nil {
		return nil, errNoSuchMailbox()
	}
	return mbox, nil
}

func (u *User) mailbox(name string) (*Mailbox, error) {
	u.mutex.Lock()
	defer u.mutex.Unlock()
	return u.mailboxLocked(name)
}

// ensureMailboxLocked creates the mailbox when missing. UIDVALIDITY grows on
// every creation so a deleted and re-created name is distinguishable.
func (u *User) ensureMailboxLocked(name string) *Mailbox {
	name = normalizeName(name)
	if mbox := u.mailboxes[name]; mbox != nil {
		return mbox
	}
	u.prevUidValidity++
	mbox := newMailbox(u, name, u.prevUidValidity)
	u.mailboxes[name] = mbox
	return mbox
}

func (u *User) Status(name string, options *imap.StatusOptions) (*imap.StatusData, error) {
	mbox, err := u.mailbox(name)
	if err != nil {
		return nil, err
	}
	return mbox.statusData(options), nil
}

func (u *User) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	if len(patterns) == 0 {
		return w.WriteList(&imap.ListData{
			Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect},
			Delim: mailboxDelim,
		})
	}

	var l []imap.ListData
	for name, mbox := range u.mailboxes {
		match := false
		for _, pattern := range patterns {
			match = imapserver.MatchList(name, mailboxDelim, ref, pattern)
			if match {
				break
			}
		}
		if !match {
			continue
		}

		data := mbox.list(options)
		if data != nil {
			l = append(l, *data)
		}
	}

	sort.Slice(l, func(i, j int) bool {
		return l[i].Mailbox < l[j].Mailbox
	})

	for _, data := range l {
		if err := w.WriteList(&data); err != nil {
			return err
		}
	}

	return nil
}

func (u *User) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	mbox, err := u.mailbox(mailbox)
	if err != nil {
		return nil, errTryCreate()
	}
	return mbox.appendLiteral(r, options)
}

func (u *User) Create(name string, options *imap.CreateOptions) error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	name = normalizeName(name)
	if u.mailboxes[name] != nil {
		return &imap.Error{
			Type: imap.StatusResponseTypeNo,
			Code: imap.ResponseCodeAlreadyExists,
			Text: "Mailbox already exists",
		}
	}

	u.ensureMailboxLocked(name)
	u.store.markDirty()
	return nil
}

func (u *User) Delete(name string) error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	name = normalizeName(name)
	if name == "INBOX" {
		return &imap.Error{
			Type: imap.StatusResponseTypeNo,
			Text: "Cannot delete INBOX",
		}
	}
	if _, err := u.mailboxLocked(name); err != nil {
		return err
	}

	delete(u.mailboxes, name)
	u.store.markDirty()
	return nil
}

// Rename moves the mailbox under a new name. Renaming INBOX moves its mail
// into the new mailbox and leaves an empty INBOX behind, per RFC 3501.
func (u *User) Rename(oldName, newName string, options *imap.RenameOptions) error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	oldName = normalizeName(oldName)
	newName = normalizeName(newName)

	mbox, err := u.mailboxLocked(oldName)
	if err != nil {
		return err
	}

	if u.mailboxes[newName] != nil {
		return &imap.Error{
			Type: imap.StatusResponseTypeNo,
			Code: imap.ResponseCodeAlreadyExists,
			Text: "Mailbox already exists",
		}
	}

	if oldName == "INBOX" {
		u.prevUidValidity++
		dest := newMailbox(u, newName, u.prevUidValidity)
		mbox.mutex.Lock()
		dest.l = mbox.l
		dest.uidNext = mbox.uidNext
		expunged := make(map[*message]struct{}, len(mbox.l))
		for _, msg := range mbox.l {
			expunged[msg] = struct{}{}
		}
		mbox.expungeLocked(expunged)
		mbox.mutex.Unlock()
		u.mailboxes[newName] = dest
		u.store.markDirty()
		return nil
	}

	mbox.rename(newName)
	u.mailboxes[newName] = mbox
	delete(u.mailboxes, oldName)
	u.store.markDirty()
	return nil
}

func (u *User) Subscribe(name string) error {
	mbox, err := u.mailbox(name)
	if err != nil {
		return err
	}
	mbox.setSubscribed(true)
	return nil
}

func (u *User) Unsubscribe(name string) error {
	mbox, err := u.mailbox(name)
	if err != nil {
		return err
	}
	mbox.setSubscribed(false)
	return nil
}

func (u *User) Namespace() (*imap.NamespaceData, error) {
	return &imap.NamespaceData{
		Personal: []imap.NamespaceDescriptor{{Delim: mailboxDelim}},
	}, nil
}
