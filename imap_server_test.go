package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/filter"
	"github.com/golang-mail/go-mail/server"
	"github.com/golang-mail/go-mail/testserver"
)

// serverBackedStore runs the test server and returns a store with one
// account pointed at it and an empty cached INBOX.
func serverBackedStore(t *testing.T) (*testserver.TestServer, *cache.Store, *cache.Account) {
	t.Helper()
	ts, err := testserver.Start(testserver.Config{Address: "127.0.0.1", Username: "u", Password: "p", TLS: "none", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	store := cache.NewStore()
	store.Backend = cache.NewMemoryBackend(t.TempDir())
	acc := &cache.Account{
		Name: "acc", Server: "127.0.0.1", Port: strconv.Itoa(ts.Port()), Username: "u", Password: "p", TLS: "none",
		Mailboxes: map[string]*cache.Mailbox{"INBOX": {Name: "INBOX", Emails: map[uint32]*cache.CachedEmail{}}},
	}
	store.Accounts = []*cache.Account{acc}
	return ts, store, acc
}

func deliver(t *testing.T, ts *testserver.TestServer, mailbox, subject string, flags ...string) uint32 {
	t.Helper()
	raw := "From: sender@x\r\nTo: u@x\r\nSubject: " + subject + "\r\nMessage-ID: <" + subject + "@x>\r\n\r\nbody of " + subject + "\r\n"
	uid, err := ts.Deliver("u", mailbox, []byte(raw), flags...)
	if err != nil {
		t.Fatal(err)
	}
	return uid
}

func syncNow(t *testing.T, store *cache.Store, acc *cache.Account, mailbox string) {
	t.Helper()
	c, err := acc.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Release(c)
	syncMailbox(store, acc, c, mailbox)
}

func cachedEmail(store *cache.Store, acc *cache.Account, mailbox string, uid uint32) *cache.CachedEmail {
	store.Mu.RLock()
	defer store.Mu.RUnlock()
	mbox := acc.Mailboxes[mailbox]
	if mbox == nil {
		return nil
	}
	return mbox.Emails[uid]
}

// serverMailbox returns uid -> flags as the server sees it.
func serverMailbox(t *testing.T, acc *cache.Account, mailbox string) map[uint32][]string {
	t.Helper()
	c, err := acc.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	st, err := c.Select(mailbox, true)
	if err != nil {
		t.Fatal(err)
	}
	out := map[uint32][]string{}
	if st.Messages == 0 {
		return out
	}
	seq := new(imap.SeqSet)
	seq.AddRange(1, st.Messages)
	ch := make(chan *imap.Message, 64)
	if err := c.Fetch(seq, []imap.FetchItem{imap.FetchUid, imap.FetchFlags}, ch); err != nil {
		t.Fatal(err)
	}
	for m := range ch {
		out[m.Uid] = m.Flags
	}
	return out
}

func TestSyncMailbox_FetchesNewMailAndRefreshesFlags(t *testing.T) {
	ts, store, acc := serverBackedStore(t)
	uidA := deliver(t, ts, "INBOX", "first", `\Seen`)
	raw := "From: a@x\r\nTo: u@x\r\nSubject: with file\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
		"--b\r\nContent-Type: application/pdf; name=\"report.pdf\"\r\nContent-Disposition: attachment; filename=\"report.pdf\"\r\n\r\n%PDF\r\n--b--\r\n"
	uidB, err := ts.Deliver("u", "INBOX", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}

	syncNow(t, store, acc, "INBOX")

	a, b := cachedEmail(store, acc, "INBOX", uidA), cachedEmail(store, acc, "INBOX", uidB)
	if a == nil || b == nil {
		t.Fatalf("cache after sync: a=%v b=%v", a, b)
	}
	if strings.TrimSpace(a.Body) != "body of first" {
		t.Fatalf("body = %q", a.Body)
	}
	if !slices.Contains(a.Flags, `\Seen`) || a.Filtered {
		t.Fatalf("a = flags %v filtered %v; want \\Seen and unfiltered", a.Flags, a.Filtered)
	}
	if a.Env().Subject != "first" || a.Env().MessageID != "<first@x>" || len(a.Env().From) != 1 {
		t.Fatalf("envelope = %+v", a.Env())
	}
	if !slices.Contains(b.Attachments, "report.pdf") {
		t.Fatalf("attachments = %v", b.Attachments)
	}
	if mb := acc.Mailboxes["INBOX"]; mb.Messages != 2 || mb.Unseen != 0 {
		t.Fatalf("mailbox counts = %d/%d; unseen only counts filtered mail", mb.Messages, mb.Unseen)
	}

	// Second round: mark both filtered, damage b's body, flag a on the
	// server, deliver c, expunge nothing.
	store.Mu.Lock()
	a.Filtered, b.Filtered = true, true
	b.Body = ""
	store.Mu.Unlock()
	c, err := acc.Connect()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("INBOX", false); err != nil {
		t.Fatal(err)
	}
	seq := new(imap.SeqSet)
	seq.AddNum(uidA)
	if err := c.UidStore(seq, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.FlaggedFlag}, nil); err != nil {
		t.Fatal(err)
	}
	c.Logout()
	uidC := deliver(t, ts, "INBOX", "third")

	syncNow(t, store, acc, "INBOX")

	a, b, cc := cachedEmail(store, acc, "INBOX", uidA), cachedEmail(store, acc, "INBOX", uidB), cachedEmail(store, acc, "INBOX", uidC)
	if !slices.Contains(a.Flags, `\Flagged`) || !a.Filtered {
		t.Fatalf("a after resync: flags %v filtered %v", a.Flags, a.Filtered)
	}
	if b.Body == "" || !b.Filtered {
		t.Fatalf("b body backfill kept Filtered? body=%q filtered=%v", b.Body, b.Filtered)
	}
	if cc == nil || cc.Filtered {
		t.Fatalf("c = %+v; new mail must arrive unfiltered", cc)
	}
	if mb := acc.Mailboxes["INBOX"]; mb.Messages != 3 || mb.Unseen != 1 {
		t.Fatalf("mailbox counts = %d/%d; want 3 messages and b as the one filtered unseen", mb.Messages, mb.Unseen)
	}
}

func TestSyncMailbox_DropsMailGoneFromServerAndHandlesEmpty(t *testing.T) {
	ts, store, acc := serverBackedStore(t)
	uid := deliver(t, ts, "INBOX", "ephemeral", `\Deleted`)
	syncNow(t, store, acc, "INBOX")
	if cachedEmail(store, acc, "INBOX", uid) == nil {
		t.Fatal("not cached after first sync")
	}

	c, err := acc.Connect()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("INBOX", false); err != nil {
		t.Fatal(err)
	}
	if err := c.Expunge(nil); err != nil {
		t.Fatal(err)
	}
	c.Logout()

	syncNow(t, store, acc, "INBOX")
	if cachedEmail(store, acc, "INBOX", uid) != nil {
		t.Fatal("expunged mail still cached")
	}
	if mb := acc.Mailboxes["INBOX"]; mb.Messages != 0 || len(mb.Emails) != 0 {
		t.Fatalf("empty mailbox cached as %+v", mb)
	}
}

func TestFilterAccount_ExecutesMoveDeleteAndFlagsOnServer(t *testing.T) {
	ts, store, acc := serverBackedStore(t)
	uidArchive := deliver(t, ts, "INBOX", "archive me")
	uidKill := deliver(t, ts, "INBOX", "kill me")
	uidRead := deliver(t, ts, "INBOX", "read me")
	uidKeep := deliver(t, ts, "INBOX", "keep me")
	syncNow(t, store, acc, "INBOX")

	subjectIs := func(v string) []filter.Condition {
		return []filter.Condition{{Field: "subject", Operator: "equals", Value: v}}
	}
	cfg := filter.Config{Filters: []filter.Filter{
		{ID: "1", Name: "archive", Mailboxes: []string{"INBOX"}, Conditions: subjectIs("archive me"), Actions: []filter.Action{{Type: "move", Value: "Archive"}}},
		{ID: "2", Name: "kill", Mailboxes: []string{"INBOX"}, Conditions: subjectIs("kill me"), Actions: []filter.Action{{Type: "permanent_delete"}}},
		{ID: "3", Name: "read", Mailboxes: []string{"INBOX"}, Conditions: subjectIs("read me"), Actions: []filter.Action{{Type: "mark_as_read"}, {Type: "flag", Value: `\Flagged`}}},
	}}

	stats := filterAccount(store, acc, cfg, true, false, "", nil, false)
	if stats.Matched != 3 {
		t.Fatalf("matched %d, want 3", stats.Matched)
	}

	inbox := serverMailbox(t, acc, "INBOX")
	if _, ok := inbox[uidArchive]; ok {
		t.Fatal("moved mail still in INBOX on the server")
	}
	if _, ok := inbox[uidKill]; ok {
		t.Fatal("permanently deleted mail still on the server")
	}
	if flags := inbox[uidRead]; !slices.Contains(flags, `\Seen`) || !slices.Contains(flags, `\Flagged`) {
		t.Fatalf("read me flags on server = %v", flags)
	}
	if flags, ok := inbox[uidKeep]; !ok || len(flags) != 0 {
		t.Fatalf("keep me = %v, %v; must be untouched", flags, ok)
	}
	// The Archive mailbox did not exist; the run must have created it.
	archive := serverMailbox(t, acc, "Archive")
	if len(archive) != 1 {
		t.Fatalf("Archive on server has %d messages, want 1", len(archive))
	}

	// Cache mirrors the server, and the moved mail was synced into Archive.
	if cachedEmail(store, acc, "INBOX", uidArchive) != nil || cachedEmail(store, acc, "INBOX", uidKill) != nil {
		t.Fatal("moved or deleted mail still cached in INBOX")
	}
	if e := cachedEmail(store, acc, "INBOX", uidRead); e == nil || !slices.Contains(e.Flags, `\Seen`) || !e.Filtered {
		t.Fatalf("cached read me = %+v", e)
	}
	store.Mu.RLock()
	archived := acc.Mailboxes["Archive"]
	store.Mu.RUnlock()
	if archived == nil || len(archived.Emails) != 1 {
		t.Fatalf("cached Archive = %+v; move follow-up sync missing", archived)
	}
	for _, e := range archived.Emails {
		if e.Env().Subject != "archive me" || !e.Filtered {
			t.Fatalf("archived email = %+v", e)
		}
	}
}

func TestFilterAccount_DeleteOnlyFlagsUntilExpunge(t *testing.T) {
	ts, store, acc := serverBackedStore(t)
	uid := deliver(t, ts, "INBOX", "trash me")
	syncNow(t, store, acc, "INBOX")

	cfg := filter.Config{Filters: []filter.Filter{{
		ID: "1", Name: "trash", Mailboxes: []string{"INBOX"},
		Conditions: []filter.Condition{{Field: "subject", Operator: "equals", Value: "trash me"}},
		Actions:    []filter.Action{{Type: "delete"}},
	}}}
	filterAccount(store, acc, cfg, true, false, "", nil, false)

	inbox := serverMailbox(t, acc, "INBOX")
	if flags, ok := inbox[uid]; !ok || !slices.Contains(flags, `\Deleted`) {
		t.Fatalf("delete must set \\Deleted and leave the mail: %v, %v", flags, ok)
	}
	if e := cachedEmail(store, acc, "INBOX", uid); e == nil || !slices.Contains(e.Flags, `\Deleted`) {
		t.Fatalf("cache = %+v", e)
	}
}

func TestIdleRefresh_SyncsFiltersAndPublishes(t *testing.T) {
	ts, store, acc := serverBackedStore(t)
	cache.ConfigDir = t.TempDir()
	filters := "filters:\n  - name: read pushed\n    mailboxes: [INBOX]\n    conditions:\n      - field: subject\n        operator: equals\n        value: pushed\n    actions:\n      - type: mark_as_read\n"
	if err := os.WriteFile(filepath.Join(cache.ConfigDir, "filters.yml"), []byte(filters), 0o644); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := server.Events.Subscribe()
	defer unsubscribe()

	uid := deliver(t, ts, "INBOX", "pushed")
	idleRefresh(store, acc, "INBOX")

	e := cachedEmail(store, acc, "INBOX", uid)
	if e == nil || !e.Filtered || !slices.Contains(e.Flags, `\Seen`) {
		t.Fatalf("cached after idle refresh = %+v; want filtered and marked read", e)
	}
	if flags := serverMailbox(t, acc, "INBOX")[uid]; !slices.Contains(flags, `\Seen`) {
		t.Fatalf("server flags = %v", flags)
	}
	select {
	case raw := <-events:
		var ev struct{ Type, Account, Mailbox string }
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != "mailbox" || ev.Account != "acc" || ev.Mailbox != "INBOX" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no mailbox event published")
	}
}

func TestPool_RecoversFromDroppedConnection(t *testing.T) {
	ts, _, acc := serverBackedStore(t)
	c, err := acc.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	acc.Release(c)
	// Kill the pooled connection from the server side; the next Acquire must
	// notice the dead NOOP and dial a fresh one.
	ts.Close()
	ts2, err := testserver.Start(testserver.Config{Address: "127.0.0.1", Port: ts.Port(), Username: "u", Password: "p", TLS: "none", StorageDir: t.TempDir()})
	if err != nil {
		t.Skipf("could not rebind %d: %v", ts.Port(), err)
	}
	defer ts2.Close()
	c2, err := acc.Acquire()
	if err != nil {
		t.Fatalf("Acquire after the server dropped the pooled connection: %v", err)
	}
	defer acc.Release(c2)
	if _, err := c2.Select("INBOX", true); err != nil {
		t.Fatal(err)
	}
}
