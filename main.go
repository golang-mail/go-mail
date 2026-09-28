package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Marlliton/slogpretty"
	"github.com/google/uuid"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"gopkg.in/yaml.v3"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"

	"github.com/golang-mail/go-mail/bayesian"
	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/filter"
	"github.com/golang-mail/go-mail/metrics"
	pluginpkg "github.com/golang-mail/go-mail/plugin"
	"github.com/golang-mail/go-mail/server"
	"github.com/golang-mail/go-mail/testserver"
	"github.com/golang-mail/go-mail/types"
)

//go:embed static
var staticFS embed.FS

var (
	contactsConfig types.ContactsConfig
	contactsMu     sync.Mutex

	// filterRunMu prevents concurrent filter runs.
	filterRunMu sync.Mutex

	// filtersRunning is set while any filter run is in progress.
	filtersRunning atomic.Bool
	// filtersHaveRun is set to true after the first filter run completes.
	filtersHaveRun atomic.Bool

	// Bayesian spam classifier and config.
	spamClassifier *bayesian.Classifier
	spamThreshold  float64
	spamMailbox    string

	// filterDebug toggles [filter-debug] slog output for the filter pipeline.
	// Sourced from server.yml `filter_debug:`. Default off.
	filterDebug bool

	// External filter plugins (NDJSON/stdio).
	pluginManager *pluginpkg.Manager
)

func loadContacts() {
	contactsMu.Lock()
	defer contactsMu.Unlock()
	data, err := os.ReadFile(cache.ConfigPath("contacts.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			contactsConfig.Groups = make(map[string]map[string][]types.Contact)
		} else {
			slog.Error("failed to read contacts file", "error", err)
			contactsConfig.Groups = make(map[string]map[string][]types.Contact)
		}
		return
	}
	if err := yaml.Unmarshal(data, &contactsConfig); err != nil {
		slog.Error("failed to unmarshal contacts", "error", err)
		contactsConfig.Groups = make(map[string]map[string][]types.Contact)
	}
	if contactsConfig.Groups == nil {
		contactsConfig.Groups = make(map[string]map[string][]types.Contact)
	}
}

func saveContacts() {
	contactsMu.Lock()
	defer contactsMu.Unlock()
	data, err := yaml.Marshal(&contactsConfig)
	if err != nil {
		slog.Error("failed to marshal contacts", "error", err)
		return
	}
	if err := cache.WriteFileAtomic(cache.ConfigPath("contacts.yaml"), data, 0o644); err != nil {
		slog.Error("failed to write contacts file", "error", err)
	}
}

func addContact(account, email, mailbox string) {
	contactsMu.Lock()
	defer contactsMu.Unlock()

	for _, ignored := range contactsConfig.IgnoreMailboxes {
		if strings.EqualFold(mailbox, ignored) {
			return
		}
	}

	if email == "" {
		return
	}
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return
	}
	domain := parts[1]
	groupName := strings.Split(domain, ".")[0]

	if contactsConfig.Groups == nil {
		contactsConfig.Groups = make(map[string]map[string][]types.Contact)
	}
	if _, ok := contactsConfig.Groups[account]; !ok {
		contactsConfig.Groups[account] = make(map[string][]types.Contact)
	}
	accountGroups := contactsConfig.Groups[account]

	group, ok := accountGroups[groupName]
	if !ok {
		group = []types.Contact{}
	}

	var contactExists bool
	for i, contact := range group {
		if contact.Email == email {
			contactExists = true
			var mailboxExists bool
			for _, mb := range contact.Mailboxes {
				if mb == mailbox {
					mailboxExists = true
					break
				}
			}
			if !mailboxExists {
				group[i].Mailboxes = append(group[i].Mailboxes, mailbox)
			}
			break
		}
	}

	if !contactExists {
		group = append(group, types.Contact{
			Email:     email,
			Mailboxes: []string{mailbox},
		})
	}
	accountGroups[groupName] = group
}

func init() {
	message.CharsetReader = charset.Reader
}

// syncAccount fetches emails from IMAP and updates the cache.
// New emails arrive with Filtered=false so they are hidden from the UI
// until filterAccount processes them.
func syncAccount(store *cache.Store, account *cache.Account) {
	slog.Debug("syncing account", "account", account.Name)
	syncStart := time.Now()
	defer func() {
		metrics.SyncDuration.WithLabelValues(account.Name).Observe(time.Since(syncStart).Seconds())
	}()

	c, err := account.Acquire()
	if err != nil {
		slog.Error("failed to connect to account", "account", account.Name, "error", err)
		metrics.IMAPErrors.WithLabelValues(account.Name, "connect").Inc()
		return
	}
	defer account.Release(c)

	// List all mailboxes
	mailboxesChan := make(chan *imap.MailboxInfo, 10)
	doneChan := make(chan error, 1)
	go func() {
		doneChan <- c.List("", "*", mailboxesChan)
	}()

	// lower-cased server name -> actual name, for case- and prefix-tolerant lookups
	existingMailboxes := make(map[string]string)
	var allMailboxNames []string
	for m := range mailboxesChan {
		existingMailboxes[strings.ToLower(m.Name)] = m.Name
		allMailboxNames = append(allMailboxNames, m.Name)
	}
	if err := <-doneChan; err != nil {
		slog.Error("failed to list mailboxes", "account", account.Name, "error", err)
		return
	}

	// Get subscribed mailboxes
	subscribedMailboxes := make(map[string]bool)
	lsubChan := make(chan *imap.MailboxInfo, 10)
	lsubDoneChan := make(chan error, 1)
	go func() {
		lsubDoneChan <- c.Lsub("", "*", lsubChan)
	}()
	for m := range lsubChan {
		subscribedMailboxes[strings.ToLower(m.Name)] = true
	}
	if err := <-lsubDoneChan; err != nil {
		slog.Error("failed to list subscribed mailboxes", "account", account.Name, "error", err)
		return
	}

	// Ensure required mailboxes exist
	for _, mailbox := range requiredMailboxes(account) {
		subscribeMailbox, exists := resolveServerMailbox(existingMailboxes, mailbox)
		if !exists {
			slog.Warn("creating missing mailbox", "mailbox", mailbox, "account", account.Name)
			created, err := cache.EnsureMailbox(c, mailbox)
			if err != nil {
				slog.Error("failed to create mailbox", "mailbox", mailbox, "account", account.Name, "error", err)
				continue
			}
			subscribeMailbox = created
		}
		if !subscribedMailboxes[strings.ToLower(subscribeMailbox)] {
			slog.Info("subscribing to mailbox", "mailbox", subscribeMailbox, "account", account.Name)
			if err := c.Subscribe(subscribeMailbox); err != nil {
				slog.Error("failed to subscribe to mailbox", "mailbox", subscribeMailbox, "account", account.Name, "error", err)
			}
		}
	}

	// Sync each mailbox
	for _, mboxName := range allMailboxNames {
		syncMailbox(store, account, c, mboxName)
	}

	// Normalize configured mailbox names
	store.Mu.Lock()
	for name := range account.Mailboxes {
		if account.DeletedMailbox != "" && account.DeletedMailbox != name && strings.EqualFold(account.DeletedMailbox, name) {
			account.DeletedMailbox = name
		}
		if account.SentMailbox != "" && account.SentMailbox != name && strings.EqualFold(account.SentMailbox, name) {
			account.SentMailbox = name
		}
		if account.DraftMailbox != "" && account.DraftMailbox != name && strings.EqualFold(account.DraftMailbox, name) {
			account.DraftMailbox = name
		}
	}
	store.Mu.Unlock()

	slog.Debug("sync complete", "account", account.Name)
}

// existingEmailsForSync returns cached emails still present on the server with
// their flags refreshed. Holds the write lock: handlers read Flags under RLock
// and delete/move handlers shrink the map.
func existingEmailsForSync(store *cache.Store, account *cache.Account, mboxName string, serverUIDs map[uint32]bool, serverFlags map[uint32][]string) (map[uint32]*cache.CachedEmail, map[uint32]bool) {
	store.Mu.Lock()
	defer store.Mu.Unlock()
	store.MarkDirty(account.Name, mboxName)
	toReplace := make(map[uint32]*cache.CachedEmail)
	filtered := make(map[uint32]bool)
	mbox := account.Mailboxes[mboxName]
	if mbox == nil {
		return toReplace, filtered
	}
	for uid, email := range mbox.Emails {
		if serverUIDs[uid] {
			email.Flags = serverFlags[uid]
			toReplace[uid] = email
			filtered[uid] = email.Filtered
		}
	}
	return toReplace, filtered
}

// syncMailbox syncs a single mailbox from IMAP to cache.
// New emails get Filtered=false; existing emails keep their Filtered state.
func syncMailbox(store *cache.Store, account *cache.Account, c *client.Client, mboxName string) {
	mbox, err := c.Select(mboxName, true)
	if err != nil {
		slog.Error("failed to select mailbox for sync", "mailbox", mboxName, "account", account.Name, "error", err)
		return
	}

	if mbox.Messages == 0 {
		store.Mu.Lock()
		store.MarkDirty(account.Name, mboxName)
		account.Mailboxes[mboxName] = &cache.Mailbox{
			Name: mboxName, Messages: 0, Unseen: 0,
			Emails: make(map[uint32]*cache.CachedEmail),
		}
		store.Mu.Unlock()
		return
	}

	// Fetch UIDs and flags
	allSeqSet := new(imap.SeqSet)
	allSeqSet.AddRange(1, mbox.Messages)
	uidMessages := make(chan *imap.Message, 100)
	uidFetchDone := make(chan error, 1)
	go func() {
		uidFetchDone <- c.Fetch(allSeqSet, []imap.FetchItem{imap.FetchUid, imap.FetchFlags}, uidMessages)
	}()

	allServerUIDs := make(map[uint32]bool)
	serverFlags := make(map[uint32][]string)
	for msg := range uidMessages {
		allServerUIDs[msg.Uid] = true
		serverFlags[msg.Uid] = msg.Flags
	}
	if err := <-uidFetchDone; err != nil {
		slog.Error("failed to fetch UIDs for sync", "mailbox", mboxName, "error", err)
		return
	}

	toReplace, wasFilteredByUID := existingEmailsForSync(store, account, mboxName, allServerUIDs, serverFlags)
	uidsToFetchBody := new(imap.SeqSet)
	newEmailsCount := 0
	for uid, email := range toReplace {
		if email.Body == "" {
			uidsToFetchBody.AddNum(uid)
			newEmailsCount++
		}
	}
	for uid := range allServerUIDs {
		if _, exists := toReplace[uid]; !exists {
			uidsToFetchBody.AddNum(uid)
			newEmailsCount++
		}
	}

	// Fetch bodies for new emails
	if !uidsToFetchBody.Empty() {
		slog.Info("fetching new emails", "count", newEmailsCount, "account", account.Name, "mailbox", mboxName)
		metrics.EmailsSynced.WithLabelValues(account.Name, mboxName).Add(float64(newEmailsCount))
		newMessages := make(chan *imap.Message, 100)
		newFetchDone := make(chan error, 1)
		go func() {
			newFetchDone <- c.UidFetch(uidsToFetchBody, []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid, imap.FetchBodyStructure, "BODY.PEEK[]", imap.FetchFlags, imap.FetchInternalDate}, newMessages)
		}()

		for msg := range newMessages {
			if msg.Envelope == nil {
				slog.Warn("skipping message without envelope", "uid", msg.Uid, "mailbox", mboxName)
				continue
			}
			for _, literal := range msg.Body {
				if literal == nil {
					continue
				}
				data, err := io.ReadAll(literal)
				if err != nil {
					slog.Error("failed to read literal", "error", err)
					continue
				}
				entity, err := message.Read(bytes.NewReader(data))
				var body, contentType string
				var headers map[string]string
				var attachments []string
				if err != nil {
					slog.Warn("message structure parse failed, using fallback", "error", err)
					body, contentType, headers, attachments = recoverBody(data)
				} else {
					headers = getHeaders(entity)
					body, contentType, attachments = getBody(entity)
				}

				// Extract contacts
				if msg.Envelope != nil {
					for _, addr := range msg.Envelope.From {
						addContact(account.Name, addr.Address(), mboxName)
					}
					for _, addr := range msg.Envelope.To {
						addContact(account.Name, addr.Address(), mboxName)
					}
					for _, addr := range msg.Envelope.Cc {
						addContact(account.Name, addr.Address(), mboxName)
					}
					for _, addr := range msg.Envelope.Bcc {
						addContact(account.Name, addr.Address(), mboxName)
					}
				}

				// Preserve Filtered flag if this was a body-backfill for an existing cached email
				wasFiltered := wasFilteredByUID[msg.Uid]
				// Messages appended without a Date header have a zero envelope date;
				// fall back to INTERNALDATE so they sort correctly.
				envDate := msg.Envelope.Date
				if envDate.IsZero() {
					envDate = msg.InternalDate
				}
				toReplace[msg.Uid] = &cache.CachedEmail{
					UID: msg.Uid,
					Envelope: &cache.Envelope{
						Date:      envDate,
						Subject:   msg.Envelope.Subject,
						From:      convertAddresses(msg.Envelope.From),
						Sender:    convertAddresses(msg.Envelope.Sender),
						ReplyTo:   convertAddresses(msg.Envelope.ReplyTo),
						To:        convertAddresses(msg.Envelope.To),
						Cc:        convertAddresses(msg.Envelope.Cc),
						Bcc:       convertAddresses(msg.Envelope.Bcc),
						InReplyTo: msg.Envelope.InReplyTo,
						MessageID: msg.Envelope.MessageId,
					},
					Body:        body,
					ContentType: contentType,
					Flags:       msg.Flags,
					Attachments: attachments,
					Headers:     headers,
					Filtered:    wasFiltered,
				}
				break
			}
		}
		if err := <-newFetchDone; err != nil {
			slog.Error("failed to fetch new messages", "mailbox", mboxName, "error", err)
		}
	}

	// Memory backend keeps a sidecar with labels/plugins_run/filtered so that
	// per-email filter state survives restarts even though bodies don't.
	if mb, ok := store.Backend.(*cache.MemoryBackend); ok {
		store.Mu.Lock()
		mb.ApplySidecar(account.Name, mboxName, toReplace)
		store.Mu.Unlock()
	}

	store.Mu.Lock()
	store.MarkDirty(account.Name, mboxName)

	// Compute unseen count from Filtered==true emails only
	var cachedUnseen uint32
	for _, email := range toReplace {
		if !email.Filtered {
			continue
		}
		seen := false
		for _, f := range email.Flags {
			if f == "\\Seen" {
				seen = true
				break
			}
		}
		if !seen {
			cachedUnseen++
		}
	}

	account.Mailboxes[mboxName] = &cache.Mailbox{
		Name:     mboxName,
		Messages: uint32(len(toReplace)),
		Unseen:   cachedUnseen,
		Emails:   toReplace,
	}
	store.Mu.Unlock()
}

// filterAccount runs all filters on unfiltered (Filtered==false) emails in the cache.
// It matches using cached data, executes IMAP actions, and sets Filtered=true.
// When processAll is true, all emails are re-evaluated (Filtered is reset to false first).
// When narrowed is true, filtersConfig is a subset of filters.yml: all emails are
// evaluated but Filtered is left untouched, so emails that never saw the full
// filter set still get it on the next periodic run.
// When onlyUIDs is non-nil, only those UIDs (in onlyMailbox) are processed.
type pendingAction struct {
	mailbox string
	uid     uint32
	action  filter.Action
}

func executeIMAPActions(c *client.Client, actions []pendingAction) {
	for _, pa := range actions {
		metrics.FilterActionsTotal.WithLabelValues(strings.ToLower(pa.action.Type)).Inc()
	}
	type mailboxActions struct {
		other map[filter.Action]*imap.SeqSet
		moves map[filter.Action]*imap.SeqSet
	}
	byMailbox := make(map[string]*mailboxActions)
	for _, pa := range actions {
		ma, ok := byMailbox[pa.mailbox]
		if !ok {
			ma = &mailboxActions{
				other: make(map[filter.Action]*imap.SeqSet),
				moves: make(map[filter.Action]*imap.SeqSet),
			}
			byMailbox[pa.mailbox] = ma
		}
		if strings.ToLower(pa.action.Type) == "move" {
			if _, ok := ma.moves[pa.action]; !ok {
				ma.moves[pa.action] = new(imap.SeqSet)
			}
			ma.moves[pa.action].AddNum(pa.uid)
		} else {
			if _, ok := ma.other[pa.action]; !ok {
				ma.other[pa.action] = new(imap.SeqSet)
			}
			ma.other[pa.action].AddNum(pa.uid)
		}
	}

	for mboxName, ma := range byMailbox {
		if _, err := c.Select(mboxName, false); err != nil {
			slog.Error("failed to select mailbox for actions", "mailbox", mboxName, "error", err)
			continue
		}
		for action, seqset := range ma.other {
			if !seqset.Empty() {
				filter.PerformAction(c, seqset, action)
			}
		}
		for action, seqset := range ma.moves {
			if !seqset.Empty() {
				filter.PerformAction(c, seqset, action)
			}
		}
		if len(ma.moves) > 0 {
			if err := c.Expunge(nil); err != nil {
				slog.Error("failed to expunge after move", "mailbox", mboxName, "error", err)
			}
		}
	}

	for mboxName := range mailboxesToExpunge(actions) {
		if _, err := c.Select(mboxName, false); err != nil {
			slog.Error("failed to select mailbox for expunge", "mailbox", mboxName, "error", err)
			continue
		}
		if err := c.Expunge(nil); err != nil {
			slog.Error("failed to expunge messages", "mailbox", mboxName, "error", err)
		}
	}
}

// markFiltered is false on narrowed runs: the emails were not evaluated against
// the full filter set, so they must stay eligible for the next full run.
// addLabel appends label unless present. Caller holds the store write lock.
func addLabel(email *cache.CachedEmail, label string) {
	for _, l := range email.Labels {
		if strings.EqualFold(l, label) {
			return
		}
	}
	email.Labels = append(email.Labels, label)
}

// clearPluginRun forgets plugin runs (all when target is empty) and unsets
// Filtered so the next pass re-evaluates. Caller holds the store write lock.
func clearPluginRun(email *cache.CachedEmail, target string) {
	if target == "" {
		email.PluginsRun = nil
	} else {
		kept := make([]string, 0, len(email.PluginsRun))
		for _, p := range email.PluginsRun {
			if !strings.EqualFold(p, target) {
				kept = append(kept, p)
			}
		}
		email.PluginsRun = kept
	}
	email.Filtered = false
}

func updateCacheAfterFilter(store *cache.Store, account *cache.Account, unfilteredByMailbox map[string]map[uint32]*cache.CachedEmail, actions []pendingAction, movedUIDs map[string]map[uint32]bool, markFiltered bool) {
	// Build a quick lookup: (mailbox, uid) -> []action
	type mboxUID struct {
		mailbox string
		uid     uint32
	}
	actionsByEmail := make(map[mboxUID][]filter.Action)
	for _, pa := range actions {
		key := mboxUID{pa.mailbox, pa.uid}
		actionsByEmail[key] = append(actionsByEmail[key], pa.action)
	}

	store.Mu.Lock()
	for mboxName, emails := range unfilteredByMailbox {
		store.MarkDirty(account.Name, mboxName)
		mbox, ok := account.Mailboxes[mboxName]
		if !ok {
			continue
		}
		for uid, email := range emails {
			if movedUIDs[mboxName] != nil && movedUIDs[mboxName][uid] {
				delete(mbox.Emails, uid)
				continue
			}
			for _, action := range actionsByEmail[mboxUID{mboxName, uid}] {
				switch strings.ToLower(action.Type) {
				case "mark_as_read":
					hasSeen := false
					for _, f := range email.Flags {
						if f == "\\Seen" {
							hasSeen = true
							break
						}
					}
					if !hasSeen {
						email.Flags = append(email.Flags, "\\Seen")
					}
				case "mark_as_unread":
					newFlags := make([]string, 0, len(email.Flags))
					for _, f := range email.Flags {
						if f != "\\Seen" {
							newFlags = append(newFlags, f)
						}
					}
					email.Flags = newFlags
				case "delete":
					hasDeleted := false
					for _, f := range email.Flags {
						if f == "\\Deleted" {
							hasDeleted = true
							break
						}
					}
					if !hasDeleted {
						email.Flags = append(email.Flags, "\\Deleted")
					}
				case "permanent_delete":
					delete(mbox.Emails, uid)
					if mbox.Messages > 0 {
						mbox.Messages--
					}
					hasSeen := false
					for _, f := range email.Flags {
						if f == "\\Seen" {
							hasSeen = true
							break
						}
					}
					if !hasSeen && mbox.Unseen > 0 {
						mbox.Unseen--
					}
				case "flag":
					hasFlag := false
					for _, f := range email.Flags {
						if f == action.Value {
							hasFlag = true
							break
						}
					}
					if !hasFlag {
						email.Flags = append(email.Flags, action.Value)
					}
				case "mark_as_spam":
					if spamClassifier != nil {
						spamClassifier.Train(server.SpamMessage(email), true)
					}
				}
			}
			if markFiltered {
				email.Filtered = true
			}
		}

		var unseen uint32
		for _, email := range mbox.Emails {
			if !email.Filtered {
				continue
			}
			seen := false
			for _, f := range email.Flags {
				if f == "\\Seen" {
					seen = true
					break
				}
			}
			if !seen {
				unseen++
			}
		}
		mbox.Unseen = unseen
		mbox.Messages = uint32(len(mbox.Emails))
	}
	store.Mu.Unlock()
}

// filterAccountStats summarizes one filterAccount call: how many emails were
// examined, how many of them matched at least one filter, and which mailboxes
// had unfiltered work (i.e. the run's footprint for this account).
type filterAccountStats struct {
	Matched   int
	Processed int
	Mailboxes map[string]bool
}

func filterAccount(store *cache.Store, account *cache.Account, filtersConfig filter.Config, processAll, narrowed bool, onlyMailbox string, onlyUIDs map[uint32]bool, verbose bool) (stats filterAccountStats) {
	stats.Mailboxes = map[string]bool{}
	matchedSet := map[string]map[uint32]bool{}
	var statsMu sync.Mutex
	defer func() {
		matched := 0
		for _, m := range matchedSet {
			matched += len(m)
		}
		stats.Matched = matched
	}()

	account.FilterMu.Lock()
	defer account.FilterMu.Unlock()

	// Compile (or look up cached) per-account pipeline: account filtering,
	// dependency graph, topological sort, wildcard targets, required mailboxes.
	compiled, ok := getCompiledPipeline(filtersConfig, account.Name)
	if !ok {
		slog.Error("cycle detected in filter dependencies", "account", account.Name)
		return
	}
	sortedFilters := compiled.Sorted
	wildcardMoveTargets := compiled.WildcardMoveTargets
	requiredMailboxes := compiled.RequiredMailboxes
	if len(requiredMailboxes) > 0 {
		c, err := account.Acquire()
		if err != nil {
			slog.Error("failed to connect for mailbox creation", "account", account.Name, "error", err)
		} else {
			// List existing
			existingMailboxes := make(map[string]bool)
			mailboxesChan := make(chan *imap.MailboxInfo, 10)
			doneChan := make(chan error, 1)
			go func() {
				doneChan <- c.List("", "*", mailboxesChan)
			}()
			for m := range mailboxesChan {
				existingMailboxes[strings.ToLower(m.Name)] = true
			}
			<-doneChan

			for mailbox := range requiredMailboxes {
				lowerMailbox := strings.ToLower(mailbox)
				if !existingMailboxes[lowerMailbox] && !existingMailboxes["inbox/"+lowerMailbox] {
					slog.Warn("creating missing mailbox", "mailbox", mailbox, "account", account.Name)
					if _, err := cache.EnsureMailbox(c, mailbox); err != nil {
						slog.Error("failed to create mailbox", "mailbox", mailbox, "account", account.Name, "error", err)
					}
				}
			}
			account.Release(c)
		}
	}

	// If processAll, reset all Filtered flags so everything gets re-evaluated.
	// Persist the reset before doing any work so a crash mid-run cannot leave
	// disk Filtered=true while the new rules have not actually evaluated yet.
	// Narrowed runs never touch Filtered; they select all emails directly below.
	if processAll && !narrowed {
		store.Mu.Lock()
		for _, mbox := range account.Mailboxes {
			for _, email := range mbox.Emails {
				email.Filtered = false
			}
		}
		store.Mu.Unlock()

		if err := store.Backend.ResetFiltered(account.Name); err != nil {
			slog.Error("failed to persist Filtered reset", "account", account.Name, "error", err)
		}
	}

	// If onlyUIDs specified, reset only those
	if onlyUIDs != nil && onlyMailbox != "" {
		store.Mu.Lock()
		if mbox, ok := account.Mailboxes[onlyMailbox]; ok {
			for uid := range onlyUIDs {
				if email, ok := mbox.Emails[uid]; ok {
					email.Filtered = false
				}
			}
		}
		store.Mu.Unlock()
	}

	// Collect unfiltered emails per mailbox
	store.Mu.RLock()
	unfilteredByMailbox := make(map[string]map[uint32]*cache.CachedEmail)
	for mboxName, mbox := range account.Mailboxes {
		for uid, email := range mbox.Emails {
			if !email.Filtered || (processAll && narrowed) {
				if unfilteredByMailbox[mboxName] == nil {
					unfilteredByMailbox[mboxName] = make(map[uint32]*cache.CachedEmail)
				}
				unfilteredByMailbox[mboxName][uid] = email
			}
		}
	}
	store.Mu.RUnlock()

	totalUnfiltered := 0
	for mboxName, emails := range unfilteredByMailbox {
		totalUnfiltered += len(emails)
		stats.Mailboxes[mboxName] = true
	}
	if totalUnfiltered == 0 {
		slog.Debug("no unfiltered emails", "account", account.Name)
		return
	}
	stats.Processed += totalUnfiltered
	slog.Debug("filtering unfiltered emails", "account", account.Name, "count", totalUnfiltered)

	// matchAndExecute runs filter matching in parallel on cached emails, then executes IMAP actions sequentially.
	matchAndExecute := func(unfilteredByMailbox map[string]map[uint32]*cache.CachedEmail) ([]pendingAction, map[string]map[uint32]bool) {
		var actionsMu sync.Mutex
		var actions []pendingAction
		// In-memory edits, applied under the store write lock after each filter.
		var mutations []func()
		movedUIDs := make(map[string]map[uint32]bool)

		// Phase 1: Match all filters against cached emails in parallel.
		// 64 goroutines is plenty to hide snapshot-copy latency without
		// spiking memory on huge mailboxes.
		sem := make(chan struct{}, 64)
		var wg sync.WaitGroup

		for _, f := range sortedFilters {
			for mboxName, emails := range unfilteredByMailbox {
				applyFilter := false
				if len(f.Mailboxes) == 0 {
					if mboxName == "INBOX" {
						applyFilter = true
					}
				} else {
					for _, fm := range f.Mailboxes {
						if filter.MatchMailbox(fm, mboxName) {
							applyFilter = true
							break
						}
					}
				}
				if applyFilter && wildcardMoveTargets[mboxName] {
					wildcardMatch := false
					for _, fm := range f.Mailboxes {
						if strings.ContainsAny(fm, "*?[") && filter.MatchMailbox(fm, mboxName) {
							wildcardMatch = true
							break
						}
					}
					if wildcardMatch {
						applyFilter = false
					}
				}
				if !applyFilter {
					if verbose {
						slog.Info("[filter-debug] filter skipped: mailbox not in scope", "filter", f.Name, "mailbox", mboxName, "filter_mailboxes", f.Mailboxes)
					}
					continue
				}

				// Match on snapshots so handlers and sync can keep writing flags and
				// labels under store.Mu; mutations below target the live email.
				snapshots := snapshotEmails(store, emails)
				for uid, email := range emails {
					actionsMu.Lock()
					if movedUIDs[mboxName] != nil && movedUIDs[mboxName][uid] {
						actionsMu.Unlock()
						if verbose {
							slog.Info("[filter-debug] filter skipped: email already moved by earlier filter", "filter", f.Name, "uid", uid, "mailbox", mboxName)
						}
						continue
					}
					actionsMu.Unlock()

					wg.Add(1)
					sem <- struct{}{}
					go func(mboxName string, uid uint32, live, email *cache.CachedEmail, f *filter.Filter) {
						defer func() { <-sem; wg.Done() }()

						matched, pluginActions := filter.MatchesCachedWithActions(email, *f, filter.PluginEvalContext{Account: account.Name, Mailbox: mboxName, Target: live})
						if verbose && !matched {
							// Re-run with logging for the not-matched case so debug output is preserved.
							_ = filter.MatchesCachedVerbose(email, *f)
						}
						if !matched {
							return
						}
						statsMu.Lock()
						if matchedSet[mboxName] == nil {
							matchedSet[mboxName] = map[uint32]bool{}
						}
						matchedSet[mboxName][uid] = true
						statsMu.Unlock()
						// Plugin-contributed actions are appended after the filter's own actions.
						effectiveActions := f.Actions
						if len(pluginActions) > 0 {
							effectiveActions = append(append([]filter.Action(nil), f.Actions...), pluginActions...)
						}

						actionStrs := make([]string, len(effectiveActions))
						for i, a := range effectiveActions {
							if a.Value != "" {
								actionStrs[i] = a.Type + ":" + a.Value
							} else {
								actionStrs[i] = a.Type
							}
						}
						subject := ""
						if email.Envelope != nil {
							subject = email.Envelope.Subject
						}
						slog.Warn("message matches filter", "uid", uid, "subject", subject, "filter", f.Name, "actions", strings.Join(actionStrs, ", "))
						metrics.FilterMatchedTotal.Inc()
						if verbose {
							slog.Info("[filter-debug] MATCHED - executing actions", "filter", f.Name, "uid", uid, "subject", subject, "actions", strings.Join(actionStrs, ", "))
						}

						actionsMu.Lock()
						defer actionsMu.Unlock()
						for _, action := range effectiveActions {
							if strings.ToLower(action.Type) == "label" && action.Value != "" {
								label := action.Value
								mutations = append(mutations, func() { addLabel(live, label) })
								// Register an entry in labels.yml so the new label
								// is editable from the Labels tab. No-op if already
								// defined; assigns a deterministic default color.
								if err := server.EnsureLabel(action.Value, "local"); err != nil {
									slog.Warn("failed to auto-register label", "label", action.Value, "error", err)
								}
								continue
							}
							if strings.ToLower(action.Type) == "flag" && action.Value != "" {
								// Custom IMAP keywords (not system flags) are surfaced
								// to the UI; register so the operator can give them a
								// color from the Labels tab.
								if !strings.HasPrefix(action.Value, `\`) {
									if err := server.EnsureLabel(action.Value, "imap"); err != nil {
										slog.Warn("failed to auto-register imap label", "flag", action.Value, "error", err)
									}
								}
							}
							if strings.ToLower(action.Type) == "plugin_clear_cache" {
								// Empty value = clear all plugins for this email; otherwise clear just one.
								target := action.Value
								mutations = append(mutations, func() { clearPluginRun(live, target) })
								if pluginManager != nil {
									pluginManager.InvalidateCache()
								}
								continue
							}
							actions = append(actions, pendingAction{mboxName, uid, action})
							if strings.ToLower(action.Type) == "move" {
								if movedUIDs[mboxName] == nil {
									movedUIDs[mboxName] = make(map[uint32]bool)
								}
								movedUIDs[mboxName][uid] = true
							}
						}
					}(mboxName, uid, email, snapshots[uid], f)
				}
			}
			// Wait for all emails to finish for this filter before moving to the next
			// (respects dependency order between filters)
			wg.Wait()
			if len(mutations) > 0 {
				store.Mu.Lock()
				for _, m := range mutations {
					m()
				}
				for mboxName := range unfilteredByMailbox {
					store.MarkDirty(account.Name, mboxName)
				}
				store.Mu.Unlock()
				mutations = mutations[:0]
			}
		}

		// Phase 2: Execute IMAP actions sequentially (all matching is done)
		if len(actions) > 0 {
			c, err := account.Acquire()
			if err != nil {
				slog.Error("failed to connect for filter actions", "account", account.Name, "error", err)
			} else {
				defer account.Release(c)
				executeIMAPActions(c, actions)
			}
		}

		// Phase 3: Update cache
		updateCacheAfterFilter(store, account, unfilteredByMailbox, actions, movedUIDs, !narrowed)

		return actions, movedUIDs
	}

	actions, _ := matchAndExecute(unfilteredByMailbox)

	// Handle move follow-ups: re-sync destination mailboxes and filter new arrivals
	moveDests := make(map[string]bool)
	for _, pa := range actions {
		if strings.ToLower(pa.action.Type) == "move" && pa.action.Value != "" {
			moveDests[pa.action.Value] = true
		}
	}

	for iteration := 0; len(moveDests) > 0 && iteration < 10; iteration++ {
		syncC, err := account.Acquire()
		if err != nil {
			slog.Error("failed to connect for move follow-up sync", "account", account.Name, "error", err)
			break
		}
		for dest := range moveDests {
			syncMailbox(store, account, syncC, dest)
		}
		account.Release(syncC)

		store.Mu.RLock()
		newUnfiltered := make(map[string]map[uint32]*cache.CachedEmail)
		for dest := range moveDests {
			if mbox, ok := account.Mailboxes[dest]; ok {
				for uid, email := range mbox.Emails {
					if !email.Filtered {
						if newUnfiltered[dest] == nil {
							newUnfiltered[dest] = make(map[uint32]*cache.CachedEmail)
						}
						newUnfiltered[dest][uid] = email
					}
				}
			}
		}
		store.Mu.RUnlock()

		hasNew := false
		for _, emails := range newUnfiltered {
			if len(emails) > 0 {
				hasNew = true
				break
			}
		}
		if !hasNew {
			break
		}

		slog.Debug("filtering move follow-up emails", "account", account.Name, "iteration", iteration+1)
		for mboxName, emails := range newUnfiltered {
			stats.Processed += len(emails)
			stats.Mailboxes[mboxName] = true
		}
		actions, _ = matchAndExecute(newUnfiltered)

		newMoveDests := make(map[string]bool)
		for _, pa := range actions {
			if strings.ToLower(pa.action.Type) == "move" && pa.action.Value != "" {
				newMoveDests[pa.action.Value] = true
			}
		}
		moveDests = newMoveDests
	}
	return
}

// runFiltersOnSelected runs all filters on specific UIDs in a single mailbox.
func runFiltersOnSelected(store *cache.Store, req server.RunSelectedRequest) {
	slog.Info("running filters on selected emails", "account", req.Account, "mailbox", req.Mailbox, "uids", req.UIDs)
	// Serialize with full runs: clearing filtersRunning from under a full run
	// would flip the UI to idle while it is still going.
	filterRunMu.Lock()
	defer filterRunMu.Unlock()
	filtersRunning.Store(true)
	defer filtersRunning.Store(false)

	filtersConfig, err := loadFiltersConfig()
	if err != nil {
		slog.Error("failed to load filters config for run-selected", "error", err)
		req.Done <- err
		return
	}

	store.Mu.RLock()
	var account *cache.Account
	for i := range store.Accounts {
		if store.Accounts[i].Name == req.Account {
			account = store.Accounts[i]
			break
		}
	}
	store.Mu.RUnlock()

	if account == nil {
		req.Done <- fmt.Errorf("account not found: %s", req.Account)
		return
	}

	onlyUIDs := make(map[uint32]bool, len(req.UIDs))
	for _, uid := range req.UIDs {
		onlyUIDs[uid] = true
	}

	// filterAccount locks account.FilterMu internally,
	// so this will wait only if THIS account is being filtered, not all accounts.
	filterAccount(store, account, filtersConfig, false, false, req.Mailbox, onlyUIDs, filterDebug)
	store.SaveCache()
	req.Done <- nil
}

func convertAddresses(addrs []*imap.Address) []*cache.Address {
	var result []*cache.Address
	for _, addr := range addrs {
		result = append(result, &cache.Address{
			PersonalName: addr.PersonalName,
			MailboxName:  addr.MailboxName,
			HostName:     addr.HostName,
		})
	}
	return result
}

func getHeaders(entity *message.Entity) map[string]string {
	headers := make(map[string]string)
	fields := entity.Header.Fields()
	for fields.Next() {
		key := fields.Key()
		val := fields.Value()
		if existing, ok := headers[key]; ok {
			headers[key] = existing + ", " + val
		} else {
			headers[key] = val
		}
	}
	return headers
}

func getBody(entity *message.Entity) (body, contentType string, attachments []string) {
	var textBody, htmlBody, calBody string
	var hasCal bool
	// Collect inline images keyed by Content-ID (without angle brackets)
	cidImages := make(map[string]string) // cid -> "data:<mime>;base64,..."

	walkFunc := func(path []int, part *message.Entity, err error) error {
		if err != nil {
			slog.Error("error walking multipart", "path", path, "error", err)
			return nil
		}

		ct, params, err := part.Header.ContentType()
		if err != nil {
			slog.Error("error getting content type", "path", path, "error", err)
			return nil
		}

		// Check for inline image with Content-ID
		cid := part.Header.Get("Content-Id")
		if cid != "" {
			cid = strings.TrimPrefix(cid, "<")
			cid = strings.TrimSuffix(cid, ">")
		}

		if strings.HasPrefix(ct, "text/plain") {
			b, err := io.ReadAll(part.Body)
			if err != nil {
				slog.Error("error reading text body", "path", path, "error", err)
			}
			textBody += string(b) + "\n"
		} else if strings.HasPrefix(ct, "text/calendar") {
			// Preserve calendar content type so the frontend can handle it
			b, err := io.ReadAll(part.Body)
			if err != nil {
				slog.Error("error reading calendar body", "path", path, "error", err)
			}
			calBody += string(b) + "\n"
			hasCal = true
		} else if strings.HasPrefix(ct, "text/html") {
			b, err := io.ReadAll(part.Body)
			if err != nil {
				slog.Error("error reading html body", "path", path, "error", err)
			}
			htmlBody += string(b) + "\n"
		} else if cid != "" && strings.HasPrefix(ct, "image/") {
			// Inline image — read and convert to data URI
			b, err := io.ReadAll(part.Body)
			if err != nil {
				slog.Error("error reading inline image", "path", path, "cid", cid, "error", err)
			} else {
				cidImages[cid] = "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(b)
			}
		} else if params["name"] != "" {
			attachments = append(attachments, params["name"])
		}

		return nil
	}

	if err := entity.Walk(walkFunc); err != nil {
		slog.Error("failed to walk entity", "error", err)
	}

	// Replace cid: references in HTML with data URIs
	if len(cidImages) > 0 && htmlBody != "" {
		for cid, dataURI := range cidImages {
			htmlBody = strings.ReplaceAll(htmlBody, "cid:"+cid, dataURI)
		}
	}

	if hasCal {
		contentType = "text/calendar"
		// Prefer plain text + calendar, else html + calendar, else just calendar
		if textBody != "" {
			body = textBody + "\n" + calBody
		} else if htmlBody != "" {
			body = htmlBody + "\n" + calBody
		} else {
			body = calBody
		}
	} else if htmlBody != "" {
		contentType = "text/html"
		body = htmlBody
	} else if textBody != "" {
		contentType = "text/plain"
		body = textBody
	}

	return body, contentType, attachments
}

// recoverBody attempts to extract a usable email body from raw data
// when the go-message MIME parser fails on malformed messages.
func recoverBody(data []byte) (body string, contentType string, headers map[string]string, attachments []string) {
	// Find header/body separator (\r\n\r\n or \n\n)
	var headerEnd, bodyStart int
	if idx := bytes.Index(data, []byte("\r\n\r\n")); idx >= 0 {
		headerEnd = idx
		bodyStart = idx + 4
	} else if idx := bytes.Index(data, []byte("\n\n")); idx >= 0 {
		headerEnd = idx
		bodyStart = idx + 2
	} else {
		return string(data), "text/plain", nil, nil
	}

	rawHeaders := string(data[:headerEnd])
	rawBody := data[bodyStart:]

	headers = parseRawHeaders(rawHeaders)

	ct := getHeaderValueCI(headers, "Content-Type")
	cte := getHeaderValueCI(headers, "Content-Transfer-Encoding")

	if ct == "" {
		return decodeTransferEncoding(rawBody, cte), "text/plain", headers, nil
	}

	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return decodeTransferEncoding(rawBody, cte), "text/plain", headers, nil
	}

	if strings.HasPrefix(mediaType, "multipart/") && params["boundary"] != "" {
		body, contentType, attachments = parseMultipartFallback(rawBody, params["boundary"])
		if body != "" {
			return body, contentType, headers, attachments
		}
	}

	return decodeTransferEncoding(rawBody, cte), mediaType, headers, nil
}

// parseRawHeaders manually parses email headers from raw text,
// handling folded (continuation) lines.
func parseRawHeaders(raw string) map[string]string {
	headers := make(map[string]string)
	var currentKey, currentVal string

	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			break
		}
		// Continuation line (starts with space or tab)
		if line[0] == ' ' || line[0] == '\t' {
			if currentKey != "" {
				currentVal += " " + strings.TrimSpace(line)
			}
			continue
		}
		// Save previous header
		if currentKey != "" {
			if existing, ok := headers[currentKey]; ok {
				headers[currentKey] = existing + ", " + currentVal
			} else {
				headers[currentKey] = currentVal
			}
		}
		// Parse new header
		colonIdx := strings.IndexByte(line, ':')
		if colonIdx < 0 {
			currentKey = ""
			continue
		}
		currentKey = strings.TrimSpace(line[:colonIdx])
		currentVal = strings.TrimSpace(line[colonIdx+1:])
	}
	if currentKey != "" {
		if existing, ok := headers[currentKey]; ok {
			headers[currentKey] = existing + ", " + currentVal
		} else {
			headers[currentKey] = currentVal
		}
	}

	return headers
}

// getHeaderValueCI does a case-insensitive header lookup.
func getHeaderValueCI(headers map[string]string, name string) string {
	if v, ok := headers[name]; ok {
		return v
	}
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// parseMultipartFallback parses a multipart body using Go's standard
// mime/multipart package, handling nested multipart and transfer encoding.
func parseMultipartFallback(body []byte, boundary string) (string, string, []string) {
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	var htmlBody, textBody string
	var attachments []string
	cidImages := make(map[string]string)

	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}

		ct := p.Header.Get("Content-Type")
		cte := p.Header.Get("Content-Transfer-Encoding")

		mediaType, params, _ := mime.ParseMediaType(ct)
		if mediaType == "" {
			mediaType = "text/plain"
		}

		// Check for Content-ID (inline image)
		cid := p.Header.Get("Content-Id")
		if cid != "" {
			cid = strings.TrimPrefix(cid, "<")
			cid = strings.TrimSuffix(cid, ">")
		}

		b, err := io.ReadAll(p)
		if err != nil {
			continue
		}

		if strings.HasPrefix(mediaType, "multipart/") && params["boundary"] != "" {
			innerBody, innerCT, innerAttach := parseMultipartFallback(b, params["boundary"])
			attachments = append(attachments, innerAttach...)
			if innerBody != "" {
				if innerCT == "text/html" {
					htmlBody += innerBody
				} else {
					textBody += innerBody
				}
			}
		} else if strings.HasPrefix(mediaType, "text/html") {
			htmlBody += decodeTransferEncoding(b, cte)
		} else if strings.HasPrefix(mediaType, "text/plain") {
			textBody += decodeTransferEncoding(b, cte)
		} else if cid != "" && strings.HasPrefix(mediaType, "image/") {
			decoded := decodeTransferEncoding(b, cte)
			cidImages[cid] = "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString([]byte(decoded))
		} else if params["name"] != "" {
			attachments = append(attachments, params["name"])
		} else {
			disp := p.Header.Get("Content-Disposition")
			if disp != "" {
				_, dispParams, _ := mime.ParseMediaType(disp)
				if dispParams["filename"] != "" {
					attachments = append(attachments, dispParams["filename"])
				}
			}
		}
	}

	// Replace cid: references in HTML with data URIs
	if len(cidImages) > 0 && htmlBody != "" {
		for cid, dataURI := range cidImages {
			htmlBody = strings.ReplaceAll(htmlBody, "cid:"+cid, dataURI)
		}
	}

	if htmlBody != "" {
		return htmlBody, "text/html", attachments
	}
	if textBody != "" {
		return textBody, "text/plain", attachments
	}
	return "", "", attachments
}

// decodeTransferEncoding decodes quoted-printable or base64 encoded content.
func decodeTransferEncoding(data []byte, encoding string) string {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data)))
		if err != nil {
			return string(data)
		}
		return string(decoded)
	case "base64":
		cleaned := strings.NewReplacer("\r", "", "\n", "", " ", "", "\t", "").Replace(string(data))
		decoded, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			// Try RawStdEncoding for base64 without padding
			decoded, err = base64.RawStdEncoding.DecodeString(cleaned)
			if err != nil {
				return string(data)
			}
		}
		return string(decoded)
	default:
		return string(data)
	}
}

// processAccount is kept only for runDeleteOldFilters which needs the old
// IMAP-based filter matching for older_than+delete filters.
// It now delegates to syncAccount + a targeted IMAP-based delete pass.
func processAccount(store *cache.Store, account *cache.Account, config filter.Config) {
	// Sync first to get latest state
	syncAccount(store, account)
	// Then filter using the cache-based approach.
	// narrowed=true: config holds only delete-old filters, so emails synced in
	// here must not be stamped Filtered before a full run has seen them.
	filterAccount(store, account, config, true, true, "", nil, filterDebug)
}

// loadFiltersConfig returns the filters that should run now: disabled and
// out-of-window filters are dropped. Use loadAllFiltersConfig to rewrite the file.
func loadFiltersConfig() (filter.Config, error) {
	cfg, _, err := loadFiltersConfigRaw()
	if err != nil {
		return cfg, err
	}
	return cfg.Active(time.Now()), nil
}

// loadAllFiltersConfig returns every filter in filters.yml, inactive ones included.
func loadAllFiltersConfig() (filter.Config, error) {
	cfg, _, err := loadFiltersConfigRaw()
	return cfg, err
}

// loadFiltersConfigRaw also returns the file bytes so callers can digest them.
func loadFiltersConfigRaw() (filter.Config, []byte, error) {
	filtersYamlFile, err := os.ReadFile(cache.ConfigPath("filters.yml"))
	if err != nil {
		return filter.Config{}, nil, fmt.Errorf("reading filters.yml: %w", err)
	}

	var filtersConfig filter.Config
	err = yaml.Unmarshal(filtersYamlFile, &filtersConfig)
	if err != nil {
		return filter.Config{}, nil, fmt.Errorf("unmarshal filters: %w", err)
	}
	return filtersConfig, filtersYamlFile, nil
}

// Compiled-filter cache. The dependency graph, topological sort, wildcard
// targets, and required-mailbox sets are deterministic functions of
// (filters.yml content × accountName) — building them every filter run is
// pure waste when filters.yml hasn't changed. Cache is keyed by a content
// hash so any edit invalidates it on the next run.
var (
	pipelineCacheMu        sync.RWMutex
	pipelineCacheVer       [32]byte
	pipelineCacheByAccount map[string]*filter.Compiled
)

// getCompiledPipeline returns the cached compiled pipeline for the given
// account, building it on miss. Returns ok=false when the filter set has a
// dependency cycle.
func getCompiledPipeline(cfg filter.Config, accountName string) (*filter.Compiled, bool) {
	// Hash on the marshaled representation. yaml.Marshal is cheap relative
	// to the filter run itself and the alternative (deep-walking Config to
	// hash field-by-field) is more code for no real win.
	cfgBytes, err := yaml.Marshal(cfg)
	if err != nil {
		// Marshal of a Config that was just unmarshaled shouldn't fail, but
		// fall back to no-cache rather than refusing to run filters.
		slog.Error("failed to hash filter config for cache key", "error", err)
		return filter.CompileForAccount(cfg, accountName)
	}
	ver := sha256.Sum256(cfgBytes)

	pipelineCacheMu.RLock()
	if pipelineCacheVer == ver {
		if c, ok := pipelineCacheByAccount[accountName]; ok {
			pipelineCacheMu.RUnlock()
			return c, true
		}
	}
	pipelineCacheMu.RUnlock()

	pipelineCacheMu.Lock()
	defer pipelineCacheMu.Unlock()
	if pipelineCacheVer != ver {
		pipelineCacheByAccount = make(map[string]*filter.Compiled)
		pipelineCacheVer = ver
	} else if c, ok := pipelineCacheByAccount[accountName]; ok {
		return c, true
	}
	c, ok := filter.CompileForAccount(cfg, accountName)
	if !ok {
		return nil, false
	}
	pipelineCacheByAccount[accountName] = c
	return c, true
}

func rearrangeFilters() error {
	filtersConfig, err := loadAllFiltersConfig()
	if err != nil {
		return err
	}
	if len(filtersConfig.Filters) == 0 {
		return nil
	}

	nodes := filter.BuildDependencyGraph(filtersConfig.Filters)
	sorted, ok := filter.TopologicalSort(nodes)
	if !ok {
		return fmt.Errorf("cycle detected in filter dependencies, skipping rearrange")
	}

	rearranged := make([]filter.Filter, len(sorted))
	for i, f := range sorted {
		rearranged[i] = *f
	}

	out, err := yaml.Marshal(filter.Config{Filters: rearranged})
	if err != nil {
		return fmt.Errorf("marshal rearranged filters: %w", err)
	}
	if err := cache.WriteFileAtomic(cache.ConfigPath("filters.yml"), out, 0o644); err != nil {
		return fmt.Errorf("write rearranged filters.yml: %w", err)
	}
	slog.Debug("rearranged filters.yml according to execution order")
	return nil
}

// runFiltersForPlugin re-runs the filters that reference a specific plugin with
// processAll=true so already-filtered emails are re-evaluated. Called on each
// tick of a plugin's rescan loop when its manifest sets rescan_interval.
// The narrowed filter set keeps the work surgical: other filters aren't touched.
func runFiltersForPlugin(store *cache.Store, pluginName string) {
	filtersConfig, err := loadFiltersConfig()
	if err != nil {
		slog.Error("plugin rescan: failed to load filters", "plugin", pluginName, "error", err)
		return
	}
	var relevant []filter.Filter
	allAccounts := false
	accountsSet := map[string]bool{}
	pluginField := "plugin:" + strings.ToLower(pluginName)
	for _, f := range filtersConfig.Filters {
		usesPlugin := false
		for _, c := range f.Conditions {
			if strings.EqualFold(strings.TrimSpace(c.Field), pluginField) {
				usesPlugin = true
				break
			}
		}
		if !usesPlugin {
			continue
		}
		relevant = append(relevant, f)
		if len(f.Accounts) == 0 {
			allAccounts = true
		} else {
			for _, a := range f.Accounts {
				accountsSet[a] = true
			}
		}
	}
	if len(relevant) == 0 {
		slog.Debug("plugin rescan: no filters reference plugin", "plugin", pluginName)
		return
	}

	filterRunMu.Lock()
	defer filterRunMu.Unlock()
	filtersRunning.Store(true)
	defer filtersRunning.Store(false)

	start := time.Now()
	narrowedCfg := filter.Config{Filters: relevant}
	agg := filterRunStats{Mailboxes: map[pluginpkg.MailboxRef]bool{}}
	for i := range store.Accounts {
		acc := store.Accounts[i]
		if !allAccounts && !accountsSet[acc.Name] {
			continue
		}
		s := filterAccount(store, acc, narrowedCfg, true, true, "", nil, filterDebug)
		agg.merge(acc.Name, s)
	}
	store.SaveCache()
	slog.Info("plugin rescan complete", "plugin", pluginName, "filters", len(relevant))
	if pluginManager != nil {
		broadcastBatchComplete("rescan", start, time.Now(), agg)
	}
}

func runFilters(store *cache.Store, processAll bool, trigger string) {
	filterRunMu.Lock()
	defer filterRunMu.Unlock()
	filtersRunning.Store(true)
	metrics.FiltersRunning.Set(1)
	defer func() {
		filtersRunning.Store(false)
		filtersHaveRun.Store(true)
		metrics.FiltersRunning.Set(0)
	}()
	start := time.Now()
	defer func() {
		metrics.FilterRunsTotal.WithLabelValues(trigger).Inc()
		metrics.FilterRunDuration.WithLabelValues(trigger).Observe(time.Since(start).Seconds())
	}()
	if processAll {
		slog.Info("running filters", "mode", "all", "trigger", trigger)
	} else {
		slog.Info("running filters", "mode", "new_only", "trigger", trigger)
	}

	filtersConfig, filtersRaw, err := loadFiltersConfigRaw()
	if err != nil {
		slog.Error("failed to load filters config", "error", err)
		return
	}
	filtersConfig = filtersConfig.Active(time.Now())
	// A restart with unchanged filters only needs to look at new mail.
	if processAll && trigger == "startup" && !fullRunNeeded(filtersStatePath(), filtersRaw) {
		slog.Info("filters unchanged since last full run, evaluating new mail only")
		processAll = false
	}

	filterNames := make([]string, len(filtersConfig.Filters))
	for i, f := range filtersConfig.Filters {
		filterNames[i] = f.Name
	}
	slog.Debug("loaded filters", "count", len(filtersConfig.Filters), "filters", filterNames)

	// Phase 1: Sync all accounts in parallel (IMAP → cache, new emails Filtered=false)
	store.SyncStatus.Store("syncing")
	var wgSync sync.WaitGroup
	for i := range store.Accounts {
		wgSync.Add(1)
		go func(account *cache.Account) {
			defer wgSync.Done()
			syncAccount(store, account)
		}(store.Accounts[i])
	}
	wgSync.Wait()
	slog.Info("sync complete, all accounts synced")

	// Phase 2: Filter all accounts in parallel (match on cache → IMAP actions → Filtered=true)
	store.SyncStatus.Store("filtering")
	var wgFilter sync.WaitGroup
	var aggMu sync.Mutex
	agg := filterRunStats{Mailboxes: map[pluginpkg.MailboxRef]bool{}}
	for i := range store.Accounts {
		wgFilter.Add(1)
		go func(account *cache.Account) {
			defer wgFilter.Done()
			s := filterAccount(store, account, filtersConfig, processAll, false, "", nil, filterDebug)
			aggMu.Lock()
			agg.merge(account.Name, s)
			aggMu.Unlock()
		}(store.Accounts[i])
	}
	wgFilter.Wait()
	slog.Info("filtering complete, all accounts filtered")
	if processAll {
		if err := recordFullRun(filtersStatePath(), filtersRaw); err != nil {
			slog.Warn("failed to record full filter run", "error", err)
		}
	}

	// Phase 3: Bayesian spam classification on INBOX emails
	if spamClassifier != nil && spamClassifier.IsTrained() {
		classifySpam(store)
	}

	// Save classifier if any training happened (mark_as_spam filter actions)
	if spamClassifier != nil {
		if err := spamClassifier.Save(); err != nil {
			slog.Error("failed to save spam classifier after filter run", "error", err)
		}
	}

	store.SyncStatus.Store("ready")

	store.SaveCache()
	saveContacts()
	updateCacheMetrics(store)
	server.PublishMailboxChange("", "")

	if pluginManager != nil {
		broadcastBatchComplete(trigger, start, time.Now(), agg)
	}
}

// filterRunStats aggregates per-account filterAccount stats into a single
// run-level view: which (account, mailbox) refs the run touched, plus
// matched/processed totals for the BatchCompleteCounts payload.
type filterRunStats struct {
	Matched   int
	Processed int
	Mailboxes map[pluginpkg.MailboxRef]bool
}

func (s *filterRunStats) merge(account string, acc filterAccountStats) {
	s.Matched += acc.Matched
	s.Processed += acc.Processed
	for mbox := range acc.Mailboxes {
		s.Mailboxes[pluginpkg.MailboxRef{Account: account, Mailbox: mbox}] = true
	}
}

// broadcastBatchComplete dispatches a batch_complete event through the plugin
// manager. Mailboxes and counts come from the run's filterRunStats so the
// payload reflects the run's actual footprint, not the full store.
func broadcastBatchComplete(trigger string, started, ended time.Time, stats filterRunStats) {
	mailboxes := make([]pluginpkg.MailboxRef, 0, len(stats.Mailboxes))
	for ref := range stats.Mailboxes {
		mailboxes = append(mailboxes, ref)
	}
	event := pluginpkg.BatchCompleteEvent{
		RunID:     uuid.NewString(),
		StartedAt: started,
		EndedAt:   ended,
		Trigger:   trigger,
		Mailboxes: mailboxes,
		Counts: pluginpkg.BatchCompleteCounts{
			Matched:   stats.Matched,
			Processed: stats.Processed,
		},
	}
	pluginManager.BroadcastBatchComplete(event)
}

// reloadPluginsConfig is invoked by the fsnotify watcher after a debounce
// when plugins.yml changes on disk. Re-reads the file, hands the new config
// to the plugin manager which diffs against the live set and spawns/respawns/
// tears down children as needed.
func reloadPluginsConfig() {
	if pluginManager == nil {
		return
	}
	path := cache.ConfigPath("plugins.yml")
	slog.Info("plugins.yml modified, reloading", "file", path)
	cfg, err := pluginpkg.LoadConfig(path)
	if err != nil {
		slog.Error("reload plugins.yml", "error", err)
		return
	}
	if err := pluginManager.ApplyConfig(cfg); err != nil {
		slog.Error("apply plugin config", "error", err)
	}
}

// labelsProviderFromYAML reads labels.yml on every invocation so plugins see
// edits the operator made after startup. Returns nil on read errors (logged)
// — that's better than caching a stale view across long-running schedules.
func labelsProviderFromYAML() []pluginpkg.LabelInfo {
	cfg, err := server.ReadLabels()
	if err != nil {
		slog.Warn("read labels.yml for host.list_labels", "error", err)
		return nil
	}
	out := make([]pluginpkg.LabelInfo, 0, len(cfg.Labels))
	for _, l := range cfg.Labels {
		out = append(out, pluginpkg.LabelInfo{
			Name:    l.Name,
			Hidden:  l.Hidden,
			Storage: l.Storage,
			Color:   l.Color,
		})
	}
	return out
}

// buildMailboxStateProvider returns a closure that snapshots the requested
// mailboxes into the plugin-protocol shape, honoring the plugin's wants list
// for body/headers/envelope inclusion.
func buildMailboxStateProvider(store *cache.Store) pluginpkg.MailboxStateProvider {
	return func(mailboxes []pluginpkg.MailboxRef, wants []string) []pluginpkg.MailboxStateEntry {
		wantEnvelope, wantHeaders, wantBody := false, false, false
		for _, w := range wants {
			switch w {
			case "envelope":
				wantEnvelope = true
			case "headers":
				wantHeaders = true
			case "body":
				wantBody = true
			}
		}
		store.Mu.RLock()
		defer store.Mu.RUnlock()
		accIdx := make(map[string]*cache.Account, len(store.Accounts))
		for i := range store.Accounts {
			accIdx[store.Accounts[i].Name] = store.Accounts[i]
		}
		out := make([]pluginpkg.MailboxStateEntry, 0, len(mailboxes))
		for _, ref := range mailboxes {
			acc, ok := accIdx[ref.Account]
			if !ok {
				continue
			}
			mbox, ok := acc.Mailboxes[ref.Mailbox]
			if !ok {
				continue
			}
			entry := pluginpkg.MailboxStateEntry{Account: ref.Account, Mailbox: ref.Mailbox}
			for uid, email := range mbox.Emails {
				ee := &pluginpkg.EvalEmail{
					UID:     uid,
					Account: ref.Account,
					Mailbox: ref.Mailbox,
					Flags:   email.Flags,
					Labels:  email.Labels,
				}
				if email.Envelope != nil {
					ee.Date = email.Envelope.Date
					if wantEnvelope {
						ee.Envelope = email.Envelope
					}
				}
				if wantHeaders {
					ee.Headers = email.Headers
				}
				if wantBody {
					ee.Body = email.Body
					ee.ContentType = email.ContentType
				}
				entry.Emails = append(entry.Emails, ee)
			}
			out = append(out, entry)
		}
		return out
	}
}

func classifySpam(store *cache.Store) {
	// Phase 1: Collect email data under read lock.
	type emailData struct {
		accountIdx  int
		accountName string
		uid         uint32
		fromEmail   string
		msg         bayesian.Message
	}
	store.Mu.RLock()
	var emails []emailData
	for i, acc := range store.Accounts {
		inbox, ok := acc.Mailboxes["INBOX"]
		if !ok {
			continue
		}
		for uid, email := range inbox.Emails {
			if email.Envelope == nil {
				continue
			}
			fromEmail := ""
			if len(email.Envelope.From) > 0 {
				f := email.Envelope.From[0]
				fromEmail = f.MailboxName + "@" + f.HostName
			}
			emails = append(emails, emailData{
				accountIdx:  i,
				accountName: acc.Name,
				uid:         uid,
				fromEmail:   fromEmail,
				msg:         server.SpamMessage(email),
			})
		}
	}
	store.Mu.RUnlock()

	// Phase 2: Classify without holding store lock. Skip safe-listed senders.
	type spamCandidate struct {
		accountIdx  int
		accountName string
		uid         uint32
	}
	var candidates []spamCandidate
	for _, e := range emails {
		if e.fromEmail != "" && spamClassifier.IsSafe(e.fromEmail) {
			continue
		}
		score := spamClassifier.Classify(e.msg)
		if score >= spamThreshold {
			candidates = append(candidates, spamCandidate{
				accountIdx:  e.accountIdx,
				accountName: e.accountName,
				uid:         e.uid,
			})
		}
	}

	if len(candidates) == 0 {
		return
	}
	slog.Info("bayesian spam detected", "count", len(candidates))

	// Phase 3: Group by account, remove from INBOX cache, batch move via single IMAP connection per account.
	byAccount := make(map[int][]uint32)
	for _, c := range candidates {
		byAccount[c.accountIdx] = append(byAccount[c.accountIdx], c.uid)
	}

	var wg sync.WaitGroup
	for accountIdx, uids := range byAccount {
		// Remove from cache
		store.Mu.Lock()
		acc := store.Accounts[accountIdx]
		store.MarkDirty(acc.Name, "INBOX")
		inbox, ok := acc.Mailboxes["INBOX"]
		if ok {
			for _, uid := range uids {
				if email, exists := inbox.Emails[uid]; exists {
					inbox.Messages--
					isUnseen := true
					for _, f := range email.Flags {
						if f == "\\Seen" {
							isUnseen = false
							break
						}
					}
					if isUnseen && inbox.Unseen > 0 {
						inbox.Unseen--
					}
					delete(inbox.Emails, uid)
				}
			}
		}
		accCopy := acc
		store.Mu.Unlock()

		if !ok {
			continue
		}

		// One IMAP connection per account for all moves
		wg.Add(1)
		go func(acc *cache.Account, uids []uint32) {
			defer wg.Done()
			imapClient, err := acc.Acquire()
			if err != nil {
				slog.Error("failed to connect for spam move", "account", acc.Name, "error", err)
				return
			}
			defer acc.Release(imapClient)
			// Resolve the effective spam mailbox name; some servers require an INBOX/ prefix.
			effectiveSpam := spamMailbox
			if err := imapClient.Create(effectiveSpam); err != nil {
				lower := strings.ToLower(err.Error())
				if strings.Contains(lower, "nonexistent namespace") {
					effectiveSpam = "INBOX/" + spamMailbox
					if err2 := imapClient.Create(effectiveSpam); err2 != nil {
						if !strings.Contains(strings.ToLower(err2.Error()), "already exists") {
							slog.Debug("spam mailbox create", "note", err2)
						}
					}
				} else if !strings.Contains(lower, "already exists") {
					slog.Debug("spam mailbox create", "note", err)
				}
			}
			if _, err := imapClient.Select("INBOX", false); err != nil {
				slog.Error("failed to select INBOX for spam move", "account", acc.Name, "error", err)
				return
			}
			seqset := new(imap.SeqSet)
			for _, uid := range uids {
				seqset.AddNum(uid)
			}
			if err := imapClient.UidMove(seqset, effectiveSpam); err != nil {
				lower := strings.ToLower(err.Error())
				// If first attempt was the unprefixed name, retry with INBOX/ prefix.
				if effectiveSpam == spamMailbox && strings.Contains(lower, "nonexistent namespace") {
					effectiveSpam = "INBOX/" + spamMailbox
					if err2 := imapClient.UidMove(seqset, effectiveSpam); err2 != nil {
						slog.Error("failed to move spam emails", "account", acc.Name, "count", len(uids), "error", err2)
					} else {
						slog.Info("moved spam emails", "account", acc.Name, "count", len(uids), "mailbox", effectiveSpam)
					}
				} else {
					slog.Error("failed to move spam emails", "account", acc.Name, "count", len(uids), "error", err)
				}
			} else {
				slog.Info("moved spam emails", "account", acc.Name, "count", len(uids), "mailbox", effectiveSpam)
			}
		}(accCopy, uids)
	}
	wg.Wait()
}

func updateCacheMetrics(store *cache.Store) {
	store.Mu.RLock()
	defer store.Mu.RUnlock()
	metrics.AccountsTotal.Set(float64(len(store.Accounts)))
	for _, acc := range store.Accounts {
		metrics.MailboxesTotal.WithLabelValues(acc.Name).Set(float64(len(acc.Mailboxes)))
		for _, mb := range acc.Mailboxes {
			metrics.CachedEmails.WithLabelValues(acc.Name, mb.Name).Set(float64(len(mb.Emails)))
			metrics.UnseenEmails.WithLabelValues(acc.Name, mb.Name).Set(float64(mb.Unseen))
		}
	}
}

// isDeleteOldFilter returns true if the filter has at least one older_than condition and at least one delete action.
func isDeleteOldFilter(f filter.Filter) bool {
	hasOlderThan := false
	for _, c := range f.Conditions {
		if strings.EqualFold(c.Operator, "older_than") {
			hasOlderThan = true
			break
		}
	}
	if !hasOlderThan {
		return false
	}
	for _, a := range f.Actions {
		if strings.EqualFold(a.Type, "delete") {
			return true
		}
	}
	return false
}

// runDeleteOldFilters runs only filters that have older_than conditions with delete actions.
// These always process all emails (age must be re-evaluated).
func runDeleteOldFilters(store *cache.Store) {
	filterRunMu.Lock()
	defer filterRunMu.Unlock()
	filtersRunning.Store(true)
	defer filtersRunning.Store(false)
	slog.Debug("running delete-old filters")

	filtersConfig, err := loadFiltersConfig()
	if err != nil {
		slog.Error("failed to load filters config for delete-old", "error", err)
		return
	}

	// Keep only filters that are delete-old filters
	var deleteFilters []filter.Filter
	for _, f := range filtersConfig.Filters {
		if isDeleteOldFilter(f) {
			deleteFilters = append(deleteFilters, f)
		}
	}

	if len(deleteFilters) == 0 {
		slog.Info("no delete-old filters found, skipping")
		return
	}

	deleteConfig := filter.Config{Filters: deleteFilters}
	deleteFilterNames := make([]string, len(deleteFilters))
	for i, f := range deleteFilters {
		deleteFilterNames[i] = f.Name
	}
	slog.Debug("running delete-old filters", "count", len(deleteFilters), "filters", deleteFilterNames)

	// Sync + filter with delete-only config (processAll=true so age is re-evaluated)
	var wg sync.WaitGroup
	for i := range store.Accounts {
		wg.Add(1)
		go func(account *cache.Account) {
			defer wg.Done()
			processAccount(store, account, deleteConfig)
		}(store.Accounts[i])
	}
	wg.Wait()

	store.SaveCache()
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		hashPasswordCommand()
		return
	}
	if err := cache.InitConfigDir(); err != nil {
		log.Fatalf("error initializing config directory: %v", err)
	}
	slog.Info("using config directory", "path", cache.ConfigDir)

	serverConfig, err := server.LoadServerConfig(cache.ConfigPath("server.yml"))
	if err != nil {
		log.Fatalf("error loading server config: %v", err)
	}

	// Initialize structured logging
	logLevel := parseLogLevel(serverConfig.LogLevel)
	handler := slogpretty.New(os.Stdout, &slogpretty.Options{
		Level:      logLevel,
		AddSource:  true,
		Colorful:   true,
		Multiline:  true,
		TimeFormat: slogpretty.DefaultTimeFormat,
	})
	slog.SetDefault(slog.New(handler))

	if serverConfig.Test != nil {
		t := serverConfig.Test
		tsCfg := testserver.Config{
			Address:    t.Address,
			Port:       t.Port,
			SMTPPort:   t.SMTPPort,
			Username:   t.Username,
			Password:   t.Password,
			TLS:        t.TLS,
			StorageDir: t.StorageDir,
		}
		for _, u := range t.Users {
			tsCfg.Users = append(tsCfg.Users, testserver.UserConfig{Username: u.Username, Password: u.Password})
		}
		for _, sd := range t.Seed {
			tsCfg.Seed = append(tsCfg.Seed, testserver.Seed{Username: sd.Username, Mailbox: sd.Mailbox, File: sd.File, Flags: sd.Flags})
		}
		ts, err := testserver.Start(tsCfg)
		if err != nil {
			slog.Error("failed to start test server", "error", err)
			os.Exit(1)
		}
		defer ts.Close()
		slog.Info("test IMAP server listening", "address", t.Address, "port", t.Port)
	}

	period, err := time.ParseDuration(serverConfig.FilterPeriod)
	if err != nil {
		slog.Error("failed to parse filter_period", "error", err)
		os.Exit(1)
	}

	var deletePeriod time.Duration
	if serverConfig.FilterDeletePeriod != "" {
		deletePeriod, err = time.ParseDuration(serverConfig.FilterDeletePeriod)
		if err != nil {
			slog.Error("failed to parse filter_delete_period", "error", err)
			os.Exit(1)
		}
	}

	store := cache.NewStore()

	// Select cache backend based on config (default: sqlite)
	dbType := serverConfig.DB
	if dbType == "" {
		dbType = "sqlite"
	}
	switch dbType {
	case "yaml":
		store.Backend = cache.NewYAMLBackend(cache.ConfigPath("db"))
	case "sqlite":
		store.Backend = cache.NewSQLiteBackend(cache.ConfigPath("db"))
	case "memory":
		// Pass a base dir so the memory backend can keep a small per-account
		// sidecar (labels, plugins_run, filtered) on disk — email bodies stay in memory.
		store.Backend = cache.NewMemoryBackend(cache.ConfigPath("db"))
	default:
		slog.Error("unknown db backend", "type", dbType)
		os.Exit(1)
	}
	defer store.Backend.Close()

	accounts, outgoing, err := cache.LoadAccounts(cache.ConfigPath("accounts.yml"))
	if err != nil {
		slog.Error("failed to load accounts", "error", err)
		os.Exit(1)
	}
	store.Accounts = accounts
	store.SetOutgoingServers(outgoing)
	slog.Info("using cache backend", "type", dbType)
	store.SyncStatus.Store("loading")
	store.LoadCache()
	loadContacts()

	// Initialize Bayesian spam classifier
	spamClassifier = bayesian.New(cache.ConfigPath("spam_model.yml"))
	if err := spamClassifier.Load(); err != nil {
		slog.Error("failed to load spam classifier", "error", err)
	} else {
		stats := spamClassifier.GetStats()
		slog.Info("loaded spam classifier", "spam", stats.SpamCount, "ham", stats.HamCount, "vocab", stats.TotalVocabSize)
	}
	spamThreshold = serverConfig.SpamThreshold
	if spamThreshold <= 0 {
		spamThreshold = 0.9 // default: only move if >=90% confident
	}
	spamMailbox = serverConfig.SpamMailbox
	filterDebug = serverConfig.FilterDebug
	if filterDebug {
		slog.Info("filter debug logging enabled (server.yml: filter_debug: true)")
	}
	if spamMailbox == "" {
		spamMailbox = "Junk"
	}

	// External filter plugins (stdio NDJSON). A plugin that fails its init handshake
	// is fatal — better to fail loudly at startup than silently skip filters that
	// reference a broken plugin.
	pluginCfg, err := pluginpkg.LoadConfig(cache.ConfigPath("plugins.yml"))
	if err != nil {
		slog.Error("failed to load plugins.yml", "error", err)
		os.Exit(1)
	}
	pluginManager = pluginpkg.NewManager("go-mail/dev")
	if err := pluginManager.Init(pluginCfg); err != nil {
		slog.Error("plugin init failed", "error", err)
		pluginManager.Shutdown()
		os.Exit(1)
	}
	defer pluginManager.Shutdown()
	pluginManager.SetMailboxStateProvider(buildMailboxStateProvider(store))
	hostAPI := pluginpkg.WithLabelsProvider(pluginpkg.NewStoreHostAPI(store), labelsProviderFromYAML)
	pluginManager.SetHostAPI(hostAPI)
	pluginManager.StartSchedules()

	filter.PluginEvaluator = newPluginEvaluator(store, pluginManager)
	filter.PluginRunsLast = func(name string) bool {
		c := pluginManager.Get(name)
		if c == nil {
			return true // unknown plugin: assume run-last (safe default for side effects)
		}
		return c.Manifest().PrefersRunLast()
	}

	// Rearrange filters.yml to match execution order if configured
	if serverConfig.Filters.Rearranged() {
		if err := rearrangeFilters(); err != nil {
			slog.Warn("failed to rearrange filters", "error", err)
		}
	}

	// Channel for triggering a full filter run from the API
	runAllCh := make(chan struct{}, 1)

	// Channel for running filters on selected emails
	runSelectedCh := make(chan server.RunSelectedRequest, 10)

	// Safety net for dirty tracking: a full save hourly and on shutdown.
	go func() {
		for range time.Tick(time.Hour) {
			store.SaveCacheAll()
		}
	}()
	// Graceful exit: block main until SIGINT/SIGTERM, then unwind so the
	// deferred watcher/plugin/backend cleanups actually run.
	sigCtx, stopSignal := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stopSignal()
	go func() {
		<-sigCtx.Done()
		stopSignal() // a second signal falls back to the default kill
	}()

	// Background: run initial filters then periodic filter loop
	go func() {
		runFilters(store, true, "startup")
		// Push sync: one IDLE connection per account on INBOX; other mailboxes
		// and IDLE-less servers still rely on the periodic loop below.
		startIdleLoops(context.Background(), store, func(acc *cache.Account, mailbox string) {
			idleRefresh(store, acc, mailbox)
		})
		for {
			time.Sleep(period)
			runFilters(store, false, "periodic")
		}
	}()

	// Filter schedules: materialize passed active_from/active_until boundaries
	// into filters.yml once a minute; the watcher then runs the usual full pass.
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			changes, err := server.SweepFilterSchedules(time.Now())
			if err != nil {
				slog.Warn("filter schedule sweep failed", "error", err)
				continue
			}
			for _, c := range changes {
				slog.Info("filter schedule reached", "change", c)
			}
		}
	}()

	// Periodic delete-old filter run (only older_than + delete filters, always processAll)
	if deletePeriod > 0 {
		go func() {
			for {
				runDeleteOldFilters(store)
				time.Sleep(deletePeriod)
			}
		}()
	}

	// Per-plugin rescan loops. Plugins that declare `rescan_interval` in their
	// init manifest get a goroutine that periodically re-runs the filters
	// referencing that plugin with processAll=true, so already-filtered emails
	// are re-evaluated and the plugin can pick up upstream changes.
	for _, status := range pluginManager.Statuses() {
		if status.Manifest == nil || strings.TrimSpace(status.Manifest.RescanInterval) == "" {
			continue
		}
		interval, err := time.ParseDuration(status.Manifest.RescanInterval)
		if err != nil || interval <= 0 {
			slog.Warn("invalid rescan_interval, ignoring", "plugin", status.Name, "value", status.Manifest.RescanInterval, "error", err)
			continue
		}
		name := status.Name
		slog.Info("plugin rescan loop scheduled", "plugin", name, "interval", interval)
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for range ticker.C {
				runFiltersForPlugin(store, name)
			}
		}()
	}

	// Listen for manual "run all" triggers from the API
	go func() {
		for range runAllCh {
			runFilters(store, true, "manual")
		}
	}()

	// Listen for "run filters on selected" requests from the API
	go func() {
		for req := range runSelectedCh {
			runFiltersOnSelected(store, req)
		}
	}()

	// Watches the directory so rename-style saves keep hot reload working.
	watcher, err := watchConfigFiles(cache.ConfigDir,
		func() { runFilters(store, true, "watcher") },
		reloadPluginsConfig)
	if err != nil {
		slog.Error("failed to watch config directory", "error", err)
		os.Exit(1)
	}
	defer watcher.Close()

	// Pass the manager directly — server's PluginManagerAPI is a small slice
	// of *plugin.Manager. nil is fine: server gracefully handles a missing
	// manager by returning empty lists from /api/plugins.
	var pluginMgrAPI server.PluginManagerAPI
	if pluginManager != nil {
		pluginMgrAPI = pluginManager
	}
	srv := server.StartWebServer(store, runAllCh, runSelectedCh, &filtersRunning, &filtersHaveRun, spamClassifier, staticFS, pluginMgrAPI)

	<-sigCtx.Done()
	slog.Info("shutting down, saving cache")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown incomplete", "error", err)
	}
	store.SaveCacheAll()
}
