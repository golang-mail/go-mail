package testserver

import (
	"bytes"
	"slices"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

const mailboxDelim rune = '/'

// Mailbox is one folder. The tracker fans out EXISTS/EXPUNGE/FETCH notices
// to every selected session, which is what drives IDLE.
type Mailbox struct {
	tracker     *imapserver.MailboxTracker
	uidValidity uint32
	user        *User

	mutex      sync.Mutex
	name       string
	subscribed bool
	l          []*message
	uidNext    imap.UID
}

func newMailbox(user *User, name string, uidValidity uint32) *Mailbox {
	return &Mailbox{
		tracker:     imapserver.NewMailboxTracker(0),
		uidValidity: uidValidity,
		user:        user,
		name:        name,
		subscribed:  true,
		uidNext:     1,
	}
}

func (mbox *Mailbox) list(options *imap.ListOptions) *imap.ListData {
	mbox.mutex.Lock()
	defer mbox.mutex.Unlock()

	if options.SelectSubscribed && !mbox.subscribed {
		return nil
	}
	if options.SelectSpecialUse {
		return nil
	}

	data := imap.ListData{
		Mailbox: mbox.name,
		Delim:   mailboxDelim,
	}
	if mbox.subscribed {
		data.Attrs = append(data.Attrs, imap.MailboxAttrSubscribed)
	}
	if options.ReturnStatus != nil {
		data.Status = mbox.statusDataLocked(options.ReturnStatus)
	}
	return &data
}

func (mbox *Mailbox) statusData(options *imap.StatusOptions) *imap.StatusData {
	mbox.mutex.Lock()
	defer mbox.mutex.Unlock()
	return mbox.statusDataLocked(options)
}

func (mbox *Mailbox) statusDataLocked(options *imap.StatusOptions) *imap.StatusData {
	data := imap.StatusData{Mailbox: mbox.name}
	if options.NumMessages {
		num := uint32(len(mbox.l))
		data.NumMessages = &num
	}
	if options.UIDNext {
		data.UIDNext = mbox.uidNext
	}
	if options.UIDValidity {
		data.UIDValidity = mbox.uidValidity
	}
	if options.NumUnseen {
		num := uint32(len(mbox.l)) - mbox.countByFlagLocked(imap.FlagSeen)
		data.NumUnseen = &num
	}
	if options.NumDeleted {
		num := mbox.countByFlagLocked(imap.FlagDeleted)
		data.NumDeleted = &num
	}
	if options.Size {
		size := mbox.sizeLocked()
		data.Size = &size
	}
	if options.NumRecent {
		num := uint32(0)
		data.NumRecent = &num
	}
	return &data
}

func (mbox *Mailbox) countByFlagLocked(flag imap.Flag) uint32 {
	var n uint32
	for _, msg := range mbox.l {
		if _, ok := msg.flags[canonicalFlag(flag)]; ok {
			n++
		}
	}
	return n
}

func (mbox *Mailbox) sizeLocked() int64 {
	var size int64
	for _, msg := range mbox.l {
		size += int64(len(msg.buf))
	}
	return size
}

func (mbox *Mailbox) appendLiteral(r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}
	return mbox.appendBytes(buf.Bytes(), options), nil
}

func (mbox *Mailbox) copyMsg(msg *message) *imap.AppendData {
	return mbox.appendBytes(msg.buf, &imap.AppendOptions{
		Time:  msg.t,
		Flags: msg.flagList(),
	})
}

// appendBytes stores a message and queues an EXISTS notice for idlers.
func (mbox *Mailbox) appendBytes(buf []byte, options *imap.AppendOptions) *imap.AppendData {
	t := options.Time
	if t.IsZero() {
		t = time.Now()
	}

	mbox.mutex.Lock()
	msg := newMessage(mbox.uidNext, buf, t, options.Flags)
	mbox.uidNext++
	mbox.l = append(mbox.l, msg)
	mbox.tracker.QueueNumMessages(uint32(len(mbox.l)))
	mbox.mutex.Unlock()

	mbox.user.store.markDirty()

	return &imap.AppendData{
		UIDValidity: mbox.uidValidity,
		UID:         msg.uid,
	}
}

func (mbox *Mailbox) rename(newName string) {
	mbox.mutex.Lock()
	mbox.name = newName
	mbox.mutex.Unlock()
}

func (mbox *Mailbox) setSubscribed(subscribed bool) {
	mbox.mutex.Lock()
	mbox.subscribed = subscribed
	mbox.mutex.Unlock()
	mbox.user.store.markDirty()
}

func (mbox *Mailbox) selectDataLocked() *imap.SelectData {
	flags := mbox.flagsLocked()

	permanentFlags := make([]imap.Flag, len(flags))
	copy(permanentFlags, flags)
	permanentFlags = append(permanentFlags, imap.FlagWildcard)

	return &imap.SelectData{
		Flags:             flags,
		PermanentFlags:    permanentFlags,
		NumMessages:       uint32(len(mbox.l)),
		FirstUnseenSeqNum: mbox.firstUnseenSeqNumLocked(),
		UIDNext:           mbox.uidNext,
		UIDValidity:       mbox.uidValidity,
	}
}

func (mbox *Mailbox) firstUnseenSeqNumLocked() uint32 {
	for i, msg := range mbox.l {
		if _, ok := msg.flags[canonicalFlag(imap.FlagSeen)]; !ok {
			return uint32(i) + 1
		}
	}
	return 0
}

func (mbox *Mailbox) flagsLocked() []imap.Flag {
	m := make(map[imap.Flag]struct{})
	for _, msg := range mbox.l {
		for flag := range msg.flags {
			m[flag] = struct{}{}
		}
	}

	l := make([]imap.Flag, 0, len(m))
	for flag := range m {
		l = append(l, flag)
	}
	slices.Sort(l)
	return l
}

func (mbox *Mailbox) expunge(uids *imap.UIDSet) {
	mbox.mutex.Lock()
	expunged := make(map[*message]struct{})
	for _, msg := range mbox.l {
		if uids != nil && !uids.Contains(msg.uid) {
			continue
		}
		if _, ok := msg.flags[canonicalFlag(imap.FlagDeleted)]; ok {
			expunged[msg] = struct{}{}
		}
	}
	if len(expunged) > 0 {
		mbox.expungeLocked(expunged)
	}
	mbox.mutex.Unlock()

	if len(expunged) > 0 {
		mbox.user.store.markDirty()
	}
}

// expungeLocked drops the given messages, queueing EXPUNGE notices in
// reverse order so sequence numbers stay valid for every session.
func (mbox *Mailbox) expungeLocked(expunged map[*message]struct{}) (seqNums []uint32) {
	var filtered []*message
	for i := len(mbox.l) - 1; i >= 0; i-- {
		msg := mbox.l[i]
		if _, ok := expunged[msg]; ok {
			seqNum := uint32(i) + 1
			seqNums = append(seqNums, seqNum)
			mbox.tracker.QueueExpunge(seqNum)
		} else {
			filtered = append(filtered, msg)
		}
	}

	for i := 0; i < len(filtered)/2; i++ {
		j := len(filtered) - i - 1
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}
	mbox.l = filtered

	return seqNums
}

func (mbox *Mailbox) newView() *mailboxView {
	return &mailboxView{
		Mailbox: mbox,
		tracker: mbox.tracker.NewSession(),
	}
}

// mailboxView is one session's selection of a Mailbox with its own queue of
// pending unilateral updates.
type mailboxView struct {
	*Mailbox
	tracker   *imapserver.SessionTracker
	searchRes imap.UIDSet
}

func (mbox *mailboxView) Close() {
	mbox.tracker.Close()
}

// Fetch snapshots the matching messages under the lock and streams them
// afterwards, so a slow client never stalls other sessions.
func (mbox *mailboxView) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	markSeen := false
	for _, bs := range options.BodySection {
		if !bs.Peek {
			markSeen = true
			break
		}
	}

	type item struct {
		seqNum uint32
		msg    *message
		flags  []imap.Flag
	}
	var items []item
	changed := false
	mbox.forEach(numSet, func(seqNum uint32, msg *message) {
		if markSeen {
			if _, seen := msg.flags[canonicalFlag(imap.FlagSeen)]; !seen {
				msg.flags[canonicalFlag(imap.FlagSeen)] = struct{}{}
				mbox.Mailbox.tracker.QueueMessageFlags(seqNum, msg.uid, msg.flagList(), nil)
				changed = true
			}
		}
		items = append(items, item{seqNum: mbox.tracker.EncodeSeqNum(seqNum), msg: msg, flags: msg.flagList()})
	})
	if changed {
		mbox.user.store.markDirty()
	}

	for _, it := range items {
		if err := it.msg.fetch(w.CreateMessage(it.seqNum), it.flags, options); err != nil {
			return err
		}
	}
	return nil
}

func (mbox *mailboxView) Search(numKind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	mbox.mutex.Lock()
	defer mbox.mutex.Unlock()

	mbox.staticSearchCriteria(criteria)

	var (
		data   imap.SearchData
		seqSet imap.SeqSet
		uidSet imap.UIDSet
	)
	for i, msg := range mbox.l {
		seqNum := mbox.tracker.EncodeSeqNum(uint32(i) + 1)

		if !msg.search(seqNum, criteria) {
			continue
		}

		// Always populate the UID set, since it may be saved later for SEARCHRES
		uidSet.AddNum(msg.uid)

		var num uint32
		switch numKind {
		case imapserver.NumKindSeq:
			if seqNum == 0 {
				continue
			}
			seqSet.AddNum(seqNum)
			num = seqNum
		case imapserver.NumKindUID:
			num = uint32(msg.uid)
		}
		if data.Min == 0 || num < data.Min {
			data.Min = num
		}
		if data.Max == 0 || num > data.Max {
			data.Max = num
		}
		data.Count++
	}

	switch numKind {
	case imapserver.NumKindSeq:
		data.All = seqSet
	case imapserver.NumKindUID:
		data.All = uidSet
	}

	if options.ReturnSave {
		mbox.searchRes = uidSet
	}

	return &data, nil
}

func (mbox *mailboxView) staticSearchCriteria(criteria *imap.SearchCriteria) {
	seqNums := make([]imap.SeqSet, 0, len(criteria.SeqNum))
	for _, seqSet := range criteria.SeqNum {
		numSet := mbox.staticNumSet(seqSet)
		switch numSet := numSet.(type) {
		case imap.SeqSet:
			seqNums = append(seqNums, numSet)
		case imap.UIDSet: // can happen with SEARCHRES
			criteria.UID = append(criteria.UID, numSet)
		}
	}
	criteria.SeqNum = seqNums

	for i, uidSet := range criteria.UID {
		criteria.UID[i] = mbox.staticNumSet(uidSet).(imap.UIDSet)
	}

	for i := range criteria.Not {
		mbox.staticSearchCriteria(&criteria.Not[i])
	}
	for i := range criteria.Or {
		for j := range criteria.Or[i] {
			mbox.staticSearchCriteria(&criteria.Or[i][j])
		}
	}
}

func (mbox *mailboxView) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	mbox.forEach(numSet, func(seqNum uint32, msg *message) {
		msg.store(flags)
		mbox.Mailbox.tracker.QueueMessageFlags(seqNum, msg.uid, msg.flagList(), mbox.tracker)
	})
	mbox.user.store.markDirty()
	if !flags.Silent {
		return mbox.Fetch(w, numSet, &imap.FetchOptions{Flags: true})
	}
	return nil
}

func (mbox *mailboxView) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	return mbox.tracker.Poll(w, allowExpunge)
}

// Idle first drains notices queued since SELECT; the tracker only wakes on
// changes that happen after it starts.
func (mbox *mailboxView) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	if err := mbox.tracker.Poll(w, true); err != nil {
		return err
	}
	return mbox.tracker.Idle(w, stop)
}

func (mbox *mailboxView) forEach(numSet imap.NumSet, f func(seqNum uint32, msg *message)) {
	mbox.mutex.Lock()
	defer mbox.mutex.Unlock()
	mbox.forEachLocked(numSet, f)
}

func (mbox *mailboxView) forEachLocked(numSet imap.NumSet, f func(seqNum uint32, msg *message)) {
	numSet = mbox.staticNumSet(numSet)

	for i, msg := range mbox.l {
		seqNum := uint32(i) + 1

		var contains bool
		switch numSet := numSet.(type) {
		case imap.SeqSet:
			seqNum := mbox.tracker.EncodeSeqNum(seqNum)
			contains = seqNum != 0 && numSet.Contains(seqNum)
		case imap.UIDSet:
			contains = numSet.Contains(msg.uid)
		}
		if !contains {
			continue
		}

		f(seqNum, msg)
	}
}

// staticNumSet resolves "*" against the current mailbox and "$" against the
// saved SEARCHRES set.
func (mbox *mailboxView) staticNumSet(numSet imap.NumSet) imap.NumSet {
	if imap.IsSearchRes(numSet) {
		return mbox.searchRes
	}

	switch numSet := numSet.(type) {
	case imap.SeqSet:
		max := uint32(len(mbox.l))
		for i := range numSet {
			r := &numSet[i]
			staticNumRange(&r.Start, &r.Stop, max)
		}
	case imap.UIDSet:
		max := uint32(mbox.uidNext) - 1
		for i := range numSet {
			r := &numSet[i]
			staticNumRange((*uint32)(&r.Start), (*uint32)(&r.Stop), max)
		}
	}

	return numSet
}

func staticNumRange(start, stop *uint32, max uint32) {
	dyn := false
	if *start == 0 {
		*start = max
		dyn = true
	}
	if *stop == 0 {
		*stop = max
		dyn = true
	}
	if dyn && *start > *stop {
		*start, *stop = *stop, *start
	}
}
