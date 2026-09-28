package testserver

import (
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// session is one IMAP connection. The embedded *User is nil until LOGIN and
// the view is nil until SELECT.
type session struct {
	*User
	*mailboxView

	st *store
}

var _ imapserver.SessionIMAP4rev2 = (*session)(nil)

func (sess *session) Close() error {
	if sess.mailboxView != nil {
		sess.mailboxView.Close()
		sess.mailboxView = nil
	}
	return nil
}

func (sess *session) Login(username, password string) error {
	u := sess.st.user(username)
	if u == nil {
		return imapserver.ErrAuthFailed
	}
	if err := u.login(username, password); err != nil {
		return err
	}
	sess.User = u
	return nil
}

func (sess *session) Select(name string, options *imap.SelectOptions) (*imap.SelectData, error) {
	mbox, err := sess.User.mailbox(name)
	if err != nil {
		return nil, err
	}
	if sess.mailboxView != nil {
		sess.mailboxView.Close()
	}
	mbox.mutex.Lock()
	defer mbox.mutex.Unlock()
	sess.mailboxView = mbox.newView()
	return mbox.selectDataLocked(), nil
}

func (sess *session) Unselect() error {
	if sess.mailboxView != nil {
		sess.mailboxView.Close()
		sess.mailboxView = nil
	}
	return nil
}

func (sess *session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	sess.mailboxView.expunge(uids)
	return nil
}

func (sess *session) destination(destName string) (*Mailbox, error) {
	dest, err := sess.User.mailbox(destName)
	if err != nil {
		return nil, errTryCreate()
	}
	if sess.mailboxView != nil && dest == sess.mailboxView.Mailbox {
		return nil, &imap.Error{
			Type: imap.StatusResponseTypeNo,
			Text: "Source and destination mailboxes are identical",
		}
	}
	return dest, nil
}

func (sess *session) Copy(numSet imap.NumSet, destName string) (*imap.CopyData, error) {
	dest, err := sess.destination(destName)
	if err != nil {
		return nil, err
	}

	var sourceUIDs, destUIDs imap.UIDSet
	var copies []*message
	sess.mailboxView.forEach(numSet, func(seqNum uint32, msg *message) {
		sourceUIDs.AddNum(msg.uid)
		copies = append(copies, msg)
	})
	// Append outside the source lock: the two mailboxes have independent locks.
	for _, msg := range copies {
		destUIDs.AddNum(dest.copyMsg(msg).UID)
	}

	return &imap.CopyData{
		UIDValidity: dest.uidValidity,
		SourceUIDs:  sourceUIDs,
		DestUIDs:    destUIDs,
	}, nil
}

func (sess *session) Move(w *imapserver.MoveWriter, numSet imap.NumSet, destName string) error {
	dest, err := sess.destination(destName)
	if err != nil {
		return err
	}

	// Copy, then expunge, taking one mailbox lock at a time so two crossing
	// moves cannot deadlock.
	src := sess.mailboxView
	var sourceUIDs, destUIDs imap.UIDSet
	var moved []*message
	src.forEach(numSet, func(seqNum uint32, msg *message) {
		moved = append(moved, msg)
	})
	expunged := make(map[*message]struct{}, len(moved))
	for _, msg := range moved {
		sourceUIDs.AddNum(msg.uid)
		destUIDs.AddNum(dest.copyMsg(msg).UID)
		expunged[msg] = struct{}{}
	}
	src.mutex.Lock()
	seqNums := src.expungeLocked(expunged)
	src.mutex.Unlock()
	sess.st.markDirty()

	err = w.WriteCopyData(&imap.CopyData{
		UIDValidity: dest.uidValidity,
		SourceUIDs:  sourceUIDs,
		DestUIDs:    destUIDs,
	})
	if err != nil {
		return err
	}

	for _, seqNum := range seqNums {
		if err := w.WriteExpunge(src.tracker.EncodeSeqNum(seqNum)); err != nil {
			return err
		}
	}
	return nil
}

func (sess *session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	if sess.mailboxView == nil {
		return nil
	}
	return sess.mailboxView.Poll(w, allowExpunge)
}

func (sess *session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	if sess.mailboxView == nil {
		<-stop
		return nil
	}
	return sess.mailboxView.Idle(w, stop)
}
