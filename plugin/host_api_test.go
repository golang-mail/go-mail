package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

// buildTestStore returns a store with two accounts ("work", "personal"),
// each with an INBOX and Archive, populated with a few emails.
func buildTestStore() *cache.Store {
	store := cache.NewStore()
	mkEmail := func(uid uint32, subject, from string, seen bool, date time.Time) *cache.CachedEmail {
		flags := []string{}
		if seen {
			flags = append(flags, "\\Seen")
		}
		return &cache.CachedEmail{
			UID:   uid,
			Flags: flags,
			Body:  "body for " + subject,
			Envelope: &cache.Envelope{
				Subject: subject,
				Date:    date,
				From:    []*cache.Address{{PersonalName: "Sender", MailboxName: from, HostName: "example.com"}},
				To:      []*cache.Address{{MailboxName: "me", HostName: "example.com"}},
			},
		}
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.Accounts = []*cache.Account{
		{
			Name:   "work",
			Avatar: "/avatars/work.png",
			Mailboxes: map[string]*cache.Mailbox{
				"INBOX": {
					Name: "INBOX", Messages: 2, Unseen: 1,
					Emails: map[uint32]*cache.CachedEmail{
						1: mkEmail(1, "Meeting", "boss", false, t0),
						2: mkEmail(2, "Lunch invite", "colleague", true, t0.Add(time.Hour)),
					},
				},
				"Archive": {
					Name: "Archive", Messages: 1, Unseen: 0,
					Emails: map[uint32]*cache.CachedEmail{
						10: mkEmail(10, "Old report", "colleague", true, t0.Add(-24*time.Hour)),
					},
				},
			},
		},
		{
			Name: "personal",
			Mailboxes: map[string]*cache.Mailbox{
				"INBOX": {
					Name: "INBOX", Messages: 1, Unseen: 1,
					Emails: map[uint32]*cache.CachedEmail{
						5: mkEmail(5, "Vacation", "friend", false, t0.Add(2*time.Hour)),
					},
				},
			},
		},
	}
	return store
}

func TestHostAPI_ListAccounts(t *testing.T) {
	api := NewStoreHostAPI(buildTestStore())
	resp, err := api.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(resp.Accounts) != 2 {
		t.Fatalf("want 2 accounts, got %d", len(resp.Accounts))
	}
	// Sorted by name
	if resp.Accounts[0].Name != "personal" || resp.Accounts[1].Name != "work" {
		t.Errorf("unexpected order: %+v", resp.Accounts)
	}
	if resp.Accounts[1].Avatar != "/avatars/work.png" {
		t.Errorf("avatar missing for work: %+v", resp.Accounts[1])
	}
	if resp.Accounts[1].Unseen != 1 {
		t.Errorf("work unseen = %d, want 1", resp.Accounts[1].Unseen)
	}
}

func TestHostAPI_ListMailboxes(t *testing.T) {
	api := NewStoreHostAPI(buildTestStore())

	resp, err := api.ListMailboxes(context.Background(), ListMailboxesReq{Account: "work"})
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(resp.Mailboxes) != 2 {
		t.Fatalf("want 2 mailboxes, got %d", len(resp.Mailboxes))
	}
	if resp.Mailboxes[0].Name != "Archive" || resp.Mailboxes[1].Name != "INBOX" {
		t.Errorf("unexpected order: %+v", resp.Mailboxes)
	}
	if resp.Mailboxes[1].Unseen != 1 || resp.Mailboxes[1].Total != 2 {
		t.Errorf("INBOX counts wrong: %+v", resp.Mailboxes[1])
	}

	if _, err := api.ListMailboxes(context.Background(), ListMailboxesReq{}); err == nil {
		t.Error("expected error for empty account")
	}
	if _, err := api.ListMailboxes(context.Background(), ListMailboxesReq{Account: "ghost"}); err == nil {
		t.Error("expected error for unknown account")
	}
}

func TestHostAPI_ListEmails(t *testing.T) {
	api := NewStoreHostAPI(buildTestStore())

	resp, err := api.ListEmails(context.Background(), ListEmailsReq{Account: "work", Mailbox: "INBOX"})
	if err != nil {
		t.Fatalf("ListEmails: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
	if len(resp.Emails) != 2 {
		t.Fatalf("got %d emails, want 2", len(resp.Emails))
	}
	// Newest first
	if resp.Emails[0].UID != 2 || resp.Emails[1].UID != 1 {
		t.Errorf("wrong order: %+v", resp.Emails)
	}

	// Unread filter
	respUnread, err := api.ListEmails(context.Background(), ListEmailsReq{Account: "work", Mailbox: "INBOX", Unread: true})
	if err != nil {
		t.Fatalf("ListEmails unread: %v", err)
	}
	if len(respUnread.Emails) != 1 || respUnread.Emails[0].UID != 1 {
		t.Errorf("unread filter wrong: %+v", respUnread.Emails)
	}

	// Pagination
	respPage, err := api.ListEmails(context.Background(), ListEmailsReq{Account: "work", Mailbox: "INBOX", Page: 2, PageSize: 1})
	if err != nil {
		t.Fatalf("ListEmails page=2: %v", err)
	}
	if len(respPage.Emails) != 1 || respPage.Emails[0].UID != 1 {
		t.Errorf("page 2 size 1 should give UID 1; got %+v", respPage.Emails)
	}
}

func TestHostAPI_GetEmail(t *testing.T) {
	api := NewStoreHostAPI(buildTestStore())

	// Default parts: envelope only, no body.
	resp, err := api.GetEmail(context.Background(), GetEmailReq{Account: "work", Mailbox: "INBOX", UID: 1})
	if err != nil {
		t.Fatalf("GetEmail: %v", err)
	}
	if resp.Envelope == nil || resp.Envelope.Subject != "Meeting" {
		t.Errorf("envelope missing: %+v", resp)
	}
	if resp.Body != "" {
		t.Errorf("body should be empty without parts=body, got %q", resp.Body)
	}

	// Opt into body
	resp, err = api.GetEmail(context.Background(), GetEmailReq{Account: "work", Mailbox: "INBOX", UID: 1, Parts: []string{"body"}})
	if err != nil {
		t.Fatalf("GetEmail body: %v", err)
	}
	if resp.Body == "" {
		t.Error("body missing when parts=body requested")
	}
	if resp.Envelope != nil {
		t.Errorf("envelope present when only body requested: %+v", resp.Envelope)
	}

	if _, err := api.GetEmail(context.Background(), GetEmailReq{Account: "work", Mailbox: "INBOX", UID: 9999}); err == nil {
		t.Error("expected error for missing UID")
	}
}

func TestHostAPI_Search(t *testing.T) {
	api := NewStoreHostAPI(buildTestStore())

	resp, err := api.Search(context.Background(), SearchReq{Account: "work", Query: "meeting"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Emails) != 1 || resp.Emails[0].UID != 1 {
		t.Errorf("expected 1 match for 'meeting' (UID 1), got %+v", resp.Emails)
	}

	// Case-insensitive across address fields
	resp, err = api.Search(context.Background(), SearchReq{Account: "work", Query: "BOSS"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Emails) != 1 || resp.Emails[0].UID != 1 {
		t.Errorf("case-insensitive sender match failed: %+v", resp.Emails)
	}

	// Body opt-in
	resp, err = api.Search(context.Background(), SearchReq{Account: "work", Query: "body for Old report", WithBody: true})
	if err != nil {
		t.Fatalf("Search with body: %v", err)
	}
	if len(resp.Emails) != 1 || resp.Emails[0].UID != 10 {
		t.Errorf("body search failed: %+v", resp.Emails)
	}
}

func TestHostAPI_ListLabels(t *testing.T) {
	// Without a provider, the response is empty but not an error.
	api := NewStoreHostAPI(buildTestStore())
	resp, err := api.ListLabels(context.Background())
	if err != nil {
		t.Fatalf("ListLabels (nil provider): %v", err)
	}
	if len(resp.Labels) != 0 {
		t.Errorf("nil provider should return no labels, got %+v", resp.Labels)
	}

	// With a provider, definitions flow through verbatim (host does NOT
	// filter hidden labels — that's the plugin's job).
	want := []LabelInfo{
		{Name: "work", Hidden: false, Storage: "local", Color: "#1a73e8"},
		{Name: "internal", Hidden: true, Storage: "local", Color: "#ea4335"},
	}
	api = WithLabelsProvider(NewStoreHostAPI(buildTestStore()), func() []LabelInfo { return want })
	resp, err = api.ListLabels(context.Background())
	if err != nil {
		t.Fatalf("ListLabels (with provider): %v", err)
	}
	if len(resp.Labels) != 2 {
		t.Fatalf("want 2 labels, got %d", len(resp.Labels))
	}
	if resp.Labels[1].Name != "internal" || !resp.Labels[1].Hidden {
		t.Errorf("hidden flag should pass through: %+v", resp.Labels[1])
	}
}

// TestHostAPI_LabelsExposedOnEmailRefs confirms CachedEmail.Labels flows out
// through ListEmails and GetEmail unchanged — the plugin sees the same label
// set the store holds.
func TestHostAPI_LabelsExposedOnEmailRefs(t *testing.T) {
	store := buildTestStore()
	store.Accounts[0].Mailboxes["INBOX"].Emails[1].Labels = []string{"urgent", "internal"}

	api := NewStoreHostAPI(store)
	listResp, err := api.ListEmails(context.Background(), ListEmailsReq{Account: "work", Mailbox: "INBOX"})
	if err != nil {
		t.Fatalf("ListEmails: %v", err)
	}
	var found *EmailRef
	for i := range listResp.Emails {
		if listResp.Emails[i].UID == 1 {
			found = &listResp.Emails[i]
			break
		}
	}
	if found == nil {
		t.Fatal("UID 1 missing from ListEmails")
	}
	if len(found.Labels) != 2 || found.Labels[0] != "urgent" || found.Labels[1] != "internal" {
		t.Errorf("ListEmails labels = %v, want [urgent internal]", found.Labels)
	}

	getResp, err := api.GetEmail(context.Background(), GetEmailReq{Account: "work", Mailbox: "INBOX", UID: 1})
	if err != nil {
		t.Fatalf("GetEmail: %v", err)
	}
	if len(getResp.Labels) != 2 {
		t.Errorf("GetEmail labels = %v, want 2 entries", getResp.Labels)
	}
}

func TestManifest_AllowsHostMethod(t *testing.T) {
	m := &Manifest{HostMethods: []string{"list_mailboxes", "list_emails"}}
	cases := []struct {
		method string
		want   bool
	}{
		{"list_mailboxes", true},
		{"host.list_mailboxes", true},
		{"list_emails", true},
		{"host.get_email", false},
		{"get_email", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := m.AllowsHostMethod(tc.method); got != tc.want {
			t.Errorf("AllowsHostMethod(%q) = %v, want %v", tc.method, got, tc.want)
		}
	}
	if (&Manifest{}).AllowsHostMethod("list_accounts") {
		t.Error("empty HostMethods should not grant anything")
	}
}

func TestTokenBucket_AllowsBurstThenLimits(t *testing.T) {
	b := newTokenBucket(2) // capacity 2, refill 2/s

	// First two calls succeed immediately.
	for i := 0; i < 2; i++ {
		ok, _ := b.Allow()
		if !ok {
			t.Fatalf("call %d: expected allow", i)
		}
	}
	// Third should be limited with a retry-after.
	ok, retry := b.Allow()
	if ok {
		t.Fatal("third call should be rate-limited")
	}
	if retry <= 0 || retry > 600*time.Millisecond {
		t.Errorf("retry out of range: %s", retry)
	}

	// After waiting, refill happens.
	time.Sleep(retry + 50*time.Millisecond)
	ok, _ = b.Allow()
	if !ok {
		t.Error("expected allow after refill window")
	}
}

func TestTokenBucket_DisabledUnlimited(t *testing.T) {
	b := newTokenBucket(-1)
	for i := 0; i < 100; i++ {
		ok, _ := b.Allow()
		if !ok {
			t.Fatalf("disabled bucket should never refuse (call %d)", i)
		}
	}
}

func TestDispatchHostMethod_RoutesAllMethods(t *testing.T) {
	api := NewStoreHostAPI(buildTestStore())
	ctx := context.Background()

	cases := []struct {
		method   string
		payload  string
		mustHave string
	}{
		{"host.list_accounts", `{}`, "work"},
		{"host.list_mailboxes", `{"account":"work"}`, "INBOX"},
		{"host.list_emails", `{"account":"work","mailbox":"INBOX"}`, `"total":2`},
		{"host.get_email", `{"account":"work","mailbox":"INBOX","uid":1}`, "Meeting"},
		{"host.search", `{"account":"work","query":"Lunch"}`, `"uid":2`},
		{"host.list_labels", `{}`, `"labels"`},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			raw, err := dispatchHostMethod(ctx, api, tc.method, json.RawMessage(tc.payload))
			if err != nil {
				t.Fatalf("%s: %v", tc.method, err)
			}
			if !strings.Contains(string(raw), tc.mustHave) {
				t.Errorf("%s response missing %q: %s", tc.method, tc.mustHave, raw)
			}
		})
	}

	if _, err := dispatchHostMethod(ctx, api, "host.bogus", json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for unknown method")
	}
}
