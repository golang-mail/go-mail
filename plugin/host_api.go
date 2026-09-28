package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

// HostAPI is the read-only data surface plugins can call via host.* RPC.
// All methods are expected to be safe under concurrent calls and must not
// mutate IMAP or persisted state — host.* is observational only.
//
// Method names on the wire are dotted ("host.list_mailboxes"); the Go API
// drops the namespace since the package itself anchors it.
type HostAPI interface {
	ListAccounts(ctx context.Context) (*ListAccountsResp, error)
	ListMailboxes(ctx context.Context, req ListMailboxesReq) (*ListMailboxesResp, error)
	ListEmails(ctx context.Context, req ListEmailsReq) (*ListEmailsResp, error)
	GetEmail(ctx context.Context, req GetEmailReq) (*GetEmailResp, error)
	Search(ctx context.Context, req SearchReq) (*SearchResp, error)
	ListLabels(ctx context.Context) (*ListLabelsResp, error)
}

// LabelInfo mirrors the per-label definition shape in labels.yml. Plugins use
// the Hidden flag to drop labels the UI suppresses, so they don't leak into
// summaries or trigger spurious dirty-fingerprint changes.
type LabelInfo struct {
	Name    string `json:"name"`
	Hidden  bool   `json:"hidden,omitempty"`
	Storage string `json:"storage,omitempty"` // "local" | "imap"
	Color   string `json:"color,omitempty"`
}

// ListLabelsResp is the response payload for host.list_labels.
type ListLabelsResp struct {
	Labels []LabelInfo `json:"labels"`
}

// LabelsProvider returns the current label definitions (typically from
// labels.yml). The Manager invokes it on every host.list_labels call so the
// response reflects edits the operator made since startup. Returning a nil
// slice is fine — it just means "no labels defined".
type LabelsProvider func() []LabelInfo

// AccountInfo summarizes one account for host.list_accounts.
type AccountInfo struct {
	Name      string `json:"name"`
	Unseen    uint32 `json:"unseen"`
	Mailboxes int    `json:"mailbox_count"`
	Avatar    string `json:"avatar,omitempty"`
}

// ListAccountsResp is the response payload for host.list_accounts.
type ListAccountsResp struct {
	Accounts []AccountInfo `json:"accounts"`
}

// MailboxInfo summarizes one mailbox for host.list_mailboxes.
type MailboxInfo struct {
	Name   string `json:"name"`
	Unseen uint32 `json:"unseen"`
	Total  uint32 `json:"total"`
}

// ListMailboxesReq is the request payload for host.list_mailboxes.
type ListMailboxesReq struct {
	Account string `json:"account"`
}

// ListMailboxesResp is the response payload for host.list_mailboxes.
type ListMailboxesResp struct {
	Mailboxes []MailboxInfo `json:"mailboxes"`
}

// EmailRef is the slimmest per-email record returned by host.list_emails:
// just the envelope and metadata, no body. Plugins call host.get_email to
// pull body/headers/content for a specific UID.
type EmailRef struct {
	UID      uint32          `json:"uid"`
	Account  string          `json:"account"`
	Mailbox  string          `json:"mailbox"`
	Envelope *cache.Envelope `json:"envelope,omitempty"`
	Flags    []string        `json:"flags,omitempty"`
	Labels   []string        `json:"labels,omitempty"`
	Date     time.Time       `json:"date"`
}

// ListEmailsReq filters and paginates host.list_emails.
//
// Page / PageSize are 1-indexed; page 0 means "no pagination" (return all,
// capped at a hard internal limit). Unread filters to emails without \Seen.
// Since, when non-zero, drops emails older than that timestamp.
type ListEmailsReq struct {
	Account  string    `json:"account"`
	Mailbox  string    `json:"mailbox"`
	Page     int       `json:"page,omitempty"`
	PageSize int       `json:"page_size,omitempty"`
	Unread   bool      `json:"unread,omitempty"`
	Since    time.Time `json:"since,omitempty"`
}

// ListEmailsResp is the response payload for host.list_emails.
type ListEmailsResp struct {
	Emails   []EmailRef `json:"emails"`
	Total    int        `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

// GetEmailReq targets a specific email. Parts opts into expensive fields:
// "envelope", "headers", "body". Defaults to ["envelope"] when nil.
type GetEmailReq struct {
	Account string   `json:"account"`
	Mailbox string   `json:"mailbox"`
	UID     uint32   `json:"uid"`
	Parts   []string `json:"parts,omitempty"`
}

// GetEmailResp is the response payload for host.get_email.
type GetEmailResp struct {
	UID         uint32            `json:"uid"`
	Account     string            `json:"account"`
	Mailbox     string            `json:"mailbox"`
	Envelope    *cache.Envelope   `json:"envelope,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
	Flags       []string          `json:"flags,omitempty"`
	Labels      []string          `json:"labels,omitempty"`
	Date        time.Time         `json:"date"`
}

// SearchReq runs a case-insensitive regex over subject + addresses (+ body
// when WithBody=true) across one account's mailboxes (or one mailbox if
// Mailbox is set).
type SearchReq struct {
	Account  string `json:"account"`
	Mailbox  string `json:"mailbox,omitempty"`
	Query    string `json:"query"`
	WithBody bool   `json:"with_body,omitempty"`
}

// SearchResp is the response payload for host.search.
type SearchResp struct {
	Emails []EmailRef `json:"emails"`
}

// Sentinel errors so callers (and the dispatcher) can distinguish auth/rate
// problems from real failures.
var (
	ErrHostMethodNotGranted = errors.New("method not granted")
	ErrHostRateLimited      = errors.New("rate_limited")
)

// hostStoreAPI is the cache.Store-backed HostAPI implementation.
// labels is optional — when nil, host.list_labels returns an empty list rather
// than failing, since labels.yml lives in the server package and not every
// caller will plumb it through.
type hostStoreAPI struct {
	store  *cache.Store
	labels LabelsProvider
}

// NewStoreHostAPI returns a HostAPI that reads from the given cache.Store.
// Locking: all methods acquire store.Mu.RLock for the duration of the call.
// The labels provider may be nil; see WithLabelsProvider to attach one.
func NewStoreHostAPI(store *cache.Store) HostAPI {
	return &hostStoreAPI{store: store}
}

// WithLabelsProvider attaches the labels.yml reader to a hostStoreAPI built by
// NewStoreHostAPI. Returns the same value (typed as HostAPI) so it can be
// chained at construction. No-op if api wasn't built here.
func WithLabelsProvider(api HostAPI, p LabelsProvider) HostAPI {
	if h, ok := api.(*hostStoreAPI); ok {
		h.labels = p
	}
	return api
}

func (h *hostStoreAPI) ListLabels(_ context.Context) (*ListLabelsResp, error) {
	if h.labels == nil {
		return &ListLabelsResp{}, nil
	}
	return &ListLabelsResp{Labels: h.labels()}, nil
}

func (h *hostStoreAPI) ListAccounts(_ context.Context) (*ListAccountsResp, error) {
	h.store.Mu.RLock()
	defer h.store.Mu.RUnlock()

	out := make([]AccountInfo, 0, len(h.store.Accounts))
	for i := range h.store.Accounts {
		acc := h.store.Accounts[i]
		var unseen uint32
		for _, mbox := range acc.Mailboxes {
			unseen += mbox.Unseen
		}
		out = append(out, AccountInfo{
			Name:      acc.Name,
			Unseen:    unseen,
			Mailboxes: len(acc.Mailboxes),
			Avatar:    acc.Avatar,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return &ListAccountsResp{Accounts: out}, nil
}

func (h *hostStoreAPI) ListMailboxes(_ context.Context, req ListMailboxesReq) (*ListMailboxesResp, error) {
	if req.Account == "" {
		return nil, errors.New("account is required")
	}
	h.store.Mu.RLock()
	defer h.store.Mu.RUnlock()

	acc := h.findAccount(req.Account)
	if acc == nil {
		return nil, fmt.Errorf("account %q not found", req.Account)
	}
	out := make([]MailboxInfo, 0, len(acc.Mailboxes))
	for _, mbox := range acc.Mailboxes {
		out = append(out, MailboxInfo{
			Name:   mbox.Name,
			Unseen: mbox.Unseen,
			Total:  mbox.Messages,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return &ListMailboxesResp{Mailboxes: out}, nil
}

// hostListEmailsHardCap bounds an unpaginated host.list_emails response so
// a misconfigured plugin can't accidentally pull the entire archive in one shot.
const hostListEmailsHardCap = 5000

func (h *hostStoreAPI) ListEmails(_ context.Context, req ListEmailsReq) (*ListEmailsResp, error) {
	if req.Account == "" || req.Mailbox == "" {
		return nil, errors.New("account and mailbox are required")
	}
	h.store.Mu.RLock()
	defer h.store.Mu.RUnlock()

	acc := h.findAccount(req.Account)
	if acc == nil {
		return nil, fmt.Errorf("account %q not found", req.Account)
	}
	mbox, ok := acc.Mailboxes[req.Mailbox]
	if !ok {
		return nil, fmt.Errorf("mailbox %q not found in account %q", req.Mailbox, req.Account)
	}

	all := make([]EmailRef, 0, len(mbox.Emails))
	for uid, email := range mbox.Emails {
		if req.Unread && hasFlag(email.Flags, "\\Seen") {
			continue
		}
		if !req.Since.IsZero() && email.Envelope != nil && email.Envelope.Date.Before(req.Since) {
			continue
		}
		ref := EmailRef{
			UID:      uid,
			Account:  acc.Name,
			Mailbox:  mbox.Name,
			Envelope: email.Envelope,
			Flags:    append([]string(nil), email.Flags...),
			Labels:   append([]string(nil), email.Labels...),
		}
		if email.Envelope != nil {
			ref.Date = email.Envelope.Date
		}
		all = append(all, ref)
	}
	// Newest first.
	sort.Slice(all, func(i, j int) bool { return all[i].Date.After(all[j].Date) })

	total := len(all)
	resp := &ListEmailsResp{Total: total, Page: req.Page, PageSize: req.PageSize}
	switch {
	case req.PageSize > 0:
		if resp.Page <= 0 {
			resp.Page = 1
		}
		start := (resp.Page - 1) * req.PageSize
		if start >= total {
			resp.Emails = []EmailRef{}
			return resp, nil
		}
		end := start + req.PageSize
		if end > total {
			end = total
		}
		resp.Emails = all[start:end]
	default:
		if total > hostListEmailsHardCap {
			resp.Emails = all[:hostListEmailsHardCap]
		} else {
			resp.Emails = all
		}
	}
	return resp, nil
}

func (h *hostStoreAPI) GetEmail(_ context.Context, req GetEmailReq) (*GetEmailResp, error) {
	if req.Account == "" || req.Mailbox == "" {
		return nil, errors.New("account and mailbox are required")
	}
	if req.UID == 0 {
		return nil, errors.New("uid is required")
	}
	wantEnvelope, wantHeaders, wantBody := partsWants(req.Parts)

	h.store.Mu.RLock()
	defer h.store.Mu.RUnlock()

	acc := h.findAccount(req.Account)
	if acc == nil {
		return nil, fmt.Errorf("account %q not found", req.Account)
	}
	mbox, ok := acc.Mailboxes[req.Mailbox]
	if !ok {
		return nil, fmt.Errorf("mailbox %q not found in account %q", req.Mailbox, req.Account)
	}
	email, ok := mbox.Emails[req.UID]
	if !ok {
		return nil, fmt.Errorf("uid %d not found in %s/%s", req.UID, req.Account, req.Mailbox)
	}

	resp := &GetEmailResp{
		UID:     req.UID,
		Account: acc.Name,
		Mailbox: mbox.Name,
		Flags:   append([]string(nil), email.Flags...),
		Labels:  append([]string(nil), email.Labels...),
	}
	if email.Envelope != nil {
		resp.Date = email.Envelope.Date
		if wantEnvelope {
			resp.Envelope = email.Envelope
		}
	}
	if wantHeaders {
		resp.Headers = email.Headers
	}
	if wantBody {
		resp.Body = email.Body
		resp.ContentType = email.ContentType
	}
	return resp, nil
}

func (h *hostStoreAPI) Search(_ context.Context, req SearchReq) (*SearchResp, error) {
	if req.Account == "" {
		return nil, errors.New("account is required")
	}
	if req.Query == "" {
		return nil, errors.New("query is required")
	}
	re, err := regexp.Compile("(?i)" + req.Query)
	if err != nil {
		return nil, fmt.Errorf("compile query: %w", err)
	}

	h.store.Mu.RLock()
	defer h.store.Mu.RUnlock()

	acc := h.findAccount(req.Account)
	if acc == nil {
		return nil, fmt.Errorf("account %q not found", req.Account)
	}

	var matches []EmailRef
	for _, mbox := range acc.Mailboxes {
		if req.Mailbox != "" && mbox.Name != req.Mailbox {
			continue
		}
		for uid, email := range mbox.Emails {
			if !emailMatchesQuery(email, re, req.WithBody) {
				continue
			}
			ref := EmailRef{
				UID:      uid,
				Account:  acc.Name,
				Mailbox:  mbox.Name,
				Envelope: email.Envelope,
				Flags:    append([]string(nil), email.Flags...),
				Labels:   append([]string(nil), email.Labels...),
			}
			if email.Envelope != nil {
				ref.Date = email.Envelope.Date
			}
			matches = append(matches, ref)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Date.After(matches[j].Date) })
	return &SearchResp{Emails: matches}, nil
}

func (h *hostStoreAPI) findAccount(name string) *cache.Account {
	for i := range h.store.Accounts {
		if h.store.Accounts[i].Name == name {
			return h.store.Accounts[i]
		}
	}
	return nil
}

func partsWants(parts []string) (envelope, headers, body bool) {
	if len(parts) == 0 {
		return true, false, false
	}
	for _, p := range parts {
		switch p {
		case "envelope":
			envelope = true
		case "headers":
			headers = true
		case "body":
			body = true
		}
	}
	return
}

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if strings.EqualFold(f, want) {
			return true
		}
	}
	return false
}

func emailMatchesQuery(email *cache.CachedEmail, re *regexp.Regexp, withBody bool) bool {
	if email.Envelope != nil {
		if re.MatchString(email.Envelope.Subject) {
			return true
		}
		for _, addr := range append(append(append(append([]*cache.Address(nil), email.Envelope.From...), email.Envelope.To...), email.Envelope.Cc...), email.Envelope.Bcc...) {
			if addr == nil {
				continue
			}
			if re.MatchString(addr.PersonalName) || re.MatchString(addr.MailboxName) || re.MatchString(addr.HostName) {
				return true
			}
			email := addr.MailboxName + "@" + addr.HostName
			if re.MatchString(email) {
				return true
			}
		}
	}
	if withBody && re.MatchString(email.Body) {
		return true
	}
	return false
}

// dispatchHostMethod parses the wire method name and payload and routes to the
// matching HostAPI call. Used by the per-plugin dispatcher.
func dispatchHostMethod(ctx context.Context, api HostAPI, method string, payload json.RawMessage) (json.RawMessage, error) {
	method = strings.TrimPrefix(method, "host.")
	switch method {
	case "list_accounts":
		resp, err := api.ListAccounts(ctx)
		return marshalOr(resp, err)
	case "list_mailboxes":
		var req ListMailboxesReq
		if err := unmarshalReq(payload, &req); err != nil {
			return nil, err
		}
		resp, err := api.ListMailboxes(ctx, req)
		return marshalOr(resp, err)
	case "list_emails":
		var req ListEmailsReq
		if err := unmarshalReq(payload, &req); err != nil {
			return nil, err
		}
		resp, err := api.ListEmails(ctx, req)
		return marshalOr(resp, err)
	case "get_email":
		var req GetEmailReq
		if err := unmarshalReq(payload, &req); err != nil {
			return nil, err
		}
		resp, err := api.GetEmail(ctx, req)
		return marshalOr(resp, err)
	case "search":
		var req SearchReq
		if err := unmarshalReq(payload, &req); err != nil {
			return nil, err
		}
		resp, err := api.Search(ctx, req)
		return marshalOr(resp, err)
	case "list_labels":
		resp, err := api.ListLabels(ctx)
		return marshalOr(resp, err)
	default:
		return nil, fmt.Errorf("unknown host method %q", method)
	}
}

func unmarshalReq(payload json.RawMessage, dst any) error {
	if len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	return nil
}

func marshalOr(v any, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
