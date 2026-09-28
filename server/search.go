package server

import (
	"encoding/json"
	"fmt"
	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/filter"
	"github.com/golang-mail/go-mail/metrics"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

func searchHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metrics.SearchQueries.Inc()
		accountName := r.URL.Query().Get("account")
		mailboxName := r.URL.Query().Get("mailbox")
		query := r.URL.Query().Get("query")

		spec, err := parseQuery(query)
		if err != nil {
			slog.Warn("invalid search query", "error", err)
			http.Error(w, "invalid query", http.StatusBadRequest)
			return
		}
		// Empty spec matches nothing, mirroring the frontend's empty-query guard.
		if spec.isEmpty() {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]"))
			return
		}

		// Plain words on an indexed backend: ask the index for candidates
		// outside the lock, then apply the remaining operators to those only.
		var candidates map[string]map[string]map[uint32]bool // account -> mailbox -> uids
		if ts, ok := store.Backend.(TextSearcher); ok && len(spec.freeWords) > 0 && !freeTextLooksLikeRegex(strings.Join(spec.freeWords, " ")) {
			store.Mu.RLock()
			names := make([]string, 0, len(store.Accounts))
			for _, acc := range store.Accounts {
				if accountName == "" || acc.Name == accountName {
					names = append(names, acc.Name)
				}
			}
			store.Mu.RUnlock()
			candidates = make(map[string]map[string]map[uint32]bool, len(names))
			for _, name := range names {
				found, err := ts.SearchText(name, mailboxName, spec.freeWords)
				if err != nil {
					slog.Warn("text index unavailable, scanning instead", "account", name, "error", err)
					candidates = nil
					break
				}
				candidates[name] = found
			}
			if candidates != nil {
				spec.freeText = nil // the index already applied the words
			}
		}

		store.Mu.RLock()

		type searchAddress struct {
			PersonalName string `json:"PersonalName"`
			MailboxName  string `json:"MailboxName"`
			HostName     string `json:"HostName"`
		}
		type searchResult struct {
			Subject     string          `json:"subject"`
			UID         uint32          `json:"uid"`
			Account     string          `json:"account"`
			Mailbox     string          `json:"mailbox"`
			From        []searchAddress `json:"from"`
			Date        time.Time       `json:"date"`
			Flags       []string        `json:"flags"`
			Attachments []string        `json:"attachments"`
		}

		results := make([]searchResult, 0, 256)

		addResult := func(email *cache.CachedEmail, accName, mbName string) {
			fromArr := make([]searchAddress, 0, len(email.Env().From))
			for _, addr := range email.Env().From {
				fromArr = append(fromArr, searchAddress{
					PersonalName: addr.PersonalName,
					MailboxName:  addr.MailboxName,
					HostName:     addr.HostName,
				})
			}
			results = append(results, searchResult{
				Subject:     email.Env().Subject,
				UID:         email.UID,
				Account:     accName,
				Mailbox:     mbName,
				From:        fromArr,
				Date:        email.Env().Date,
				Flags:       email.Flags,
				Attachments: email.Attachments,
			})
		}

		// A regex sweep reads every body; snapshot the scope under the lock
		// and match outside it so writers aren't stalled for the whole scan.
		if candidates == nil && spec.freeText != nil {
			type scopedEmail struct {
				acc, mb  string
				snapshot *cache.CachedEmail
			}
			var scope []scopedEmail
			for _, acc := range store.Accounts {
				if accountName != "" && acc.Name != accountName {
					continue
				}
				for _, mailbox := range acc.Mailboxes {
					if mailboxName != "" && mailbox.Name != mailboxName {
						continue
					}
					for _, email := range mailbox.Emails {
						if email.Filtered {
							scope = append(scope, scopedEmail{acc.Name, mailbox.Name, email.Snapshot()})
						}
					}
				}
			}
			store.Mu.RUnlock()
			for _, se := range scope {
				if matchesSpec(se.snapshot, spec) {
					addResult(se.snapshot, se.acc, se.mb)
				}
			}
			sort.Slice(results, func(i, j int) bool {
				return results[i].Date.After(results[j].Date)
			})
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(results)
			return
		}
		defer store.Mu.RUnlock()

		// An empty account searches every account; an empty mailbox every mailbox.
		accounts := store.Accounts
		if accountName != "" {
			accounts = nil
			if acc := findAccount(store, accountName); acc != nil {
				accounts = []*cache.Account{acc}
			}
		}
		for _, acc := range accounts {
			for _, mailbox := range acc.Mailboxes {
				if mailboxName != "" && mailbox.Name != mailboxName {
					continue
				}
				if candidates != nil {
					for uid := range candidates[acc.Name][mailbox.Name] {
						if email, ok := mailbox.Emails[uid]; ok && email.Filtered && matchesSpec(email, spec) {
							addResult(email, acc.Name, mailbox.Name)
						}
					}
					continue
				}
				for _, email := range mailbox.Emails {
					if email.Filtered && matchesSpec(email, spec) {
						addResult(email, acc.Name, mailbox.Name)
					}
				}
			}
		}

		sort.Slice(results, func(i, j int) bool {
			return results[i].Date.After(results[j].Date)
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)
	}
}

// matchesRE checks an already-compiled regex against an email's searchable fields.
func matchesRE(email *cache.CachedEmail, re *regexp.Regexp) bool {
	if re.MatchString(email.Env().Subject) {
		return true
	}
	if re.MatchString(email.Body) {
		return true
	}
	for _, addr := range email.Env().From {
		if matchAddress(re, addr) {
			return true
		}
	}
	for _, addr := range email.Env().To {
		if matchAddress(re, addr) {
			return true
		}
	}
	for _, addr := range email.Env().Cc {
		if matchAddress(re, addr) {
			return true
		}
	}
	for _, addr := range email.Env().Bcc {
		if matchAddress(re, addr) {
			return true
		}
	}
	return false
}

func matchAddress(re *regexp.Regexp, addr *cache.Address) bool {
	if re.MatchString(addr.PersonalName) {
		return true
	}
	if re.MatchString(addr.MailboxName) {
		return true
	}
	if re.MatchString(addr.HostName) {
		return true
	}
	// Match full email address
	return re.MatchString(addr.MailboxName + "@" + addr.HostName)
}

// searchSpec is the parsed form of a search query: zero or more field
// operators (ANDed together, Gmail semantics) plus a free-text regex built
// from any bare words.
type searchSpec struct {
	from          []string
	to            []string // matched against To+Cc+Bcc
	subject       []string
	label         []string
	hasAttachment *bool          // non-nil => require/forbid attachments
	unread        *bool          // true = unread, false = read
	olderThan     *time.Duration // email older than this (by Envelope.Date)
	newerThan     *time.Duration // email newer than this
	freeText      *regexp.Regexp // nil when there are no bare words
	freeWords     []string       // the bare words/phrases behind freeText
	negated       []searchSpec   // `-op:value` tokens; a match on any excludes the mail
}

// TextSearcher is implemented by backends with a full-text index.
type TextSearcher interface {
	SearchText(account, mailbox string, words []string) (map[string]map[uint32]bool, error)
}

// freeTextLooksLikeRegex reports whether the bare words use regex syntax, in
// which case the index (token matching) cannot answer and we scan instead.
func freeTextLooksLikeRegex(expr string) bool {
	return strings.ContainsAny(expr, `.^$*+?()[]{}|\`)
}

func (s searchSpec) isEmpty() bool {
	return len(s.from) == 0 && len(s.to) == 0 && len(s.subject) == 0 &&
		len(s.label) == 0 && s.hasAttachment == nil && s.unread == nil &&
		s.olderThan == nil && s.newerThan == nil && s.freeText == nil &&
		len(s.freeWords) == 0 && len(s.negated) == 0
}

var knownSearchOps = map[string]bool{
	"from": true, "to": true, "subject": true, "label": true,
	"has": true, "is": true, "older_than": true, "newer_than": true,
}

// maxSearchPatternLen caps the free-text regex so a huge pattern can't make
// the body scan disproportionately expensive. RE2 prevents pathological
// backtracking; this only bounds pattern size.
const maxSearchPatternLen = 256

// applyOperator records one known operator on spec. A bad value for a known
// operator is an error rather than silently becoming free text.
func applyOperator(spec *searchSpec, op, val string) error {
	switch op {
	case "from":
		spec.from = append(spec.from, val)
	case "to":
		spec.to = append(spec.to, val)
	case "subject":
		spec.subject = append(spec.subject, val)
	case "label":
		spec.label = append(spec.label, val)
	case "has":
		if !strings.EqualFold(val, "attachment") {
			return fmt.Errorf("has: supports only attachment, got %q", val)
		}
		t := true
		spec.hasAttachment = &t
	case "is":
		switch strings.ToLower(val) {
		case "unread":
			b := true
			spec.unread = &b
		case "read":
			b := false
			spec.unread = &b
		default:
			return fmt.Errorf("is: supports read or unread, got %q", val)
		}
	case "older_than", "newer_than":
		d, err := filter.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("%s: %q is not a duration like 30d or 2w", op, val)
		}
		if op == "older_than" {
			spec.olderThan = &d
		} else {
			spec.newerThan = &d
		}
	}
	return nil
}

// parseQuery tokenizes raw (respecting double quotes), pulls out known
// operators (a leading '-' negates one), and compiles the remaining bare
// words into the free-text regex.
func parseQuery(raw string) (searchSpec, error) {
	var spec searchSpec
	var freeWords []string
	for _, tok := range tokenizeQuery(raw) {
		body, negate := strings.CutPrefix(tok, "-")
		if negate && body == "" {
			freeWords = append(freeWords, tok)
			continue
		}
		op, val, isOp := splitOperator(body)
		if !isOp || !knownSearchOps[op] {
			// Not an operator (or an unknown name such as a URL scheme): free text.
			freeWords = append(freeWords, tok)
			continue
		}
		target := &spec
		if negate {
			target = &searchSpec{}
		}
		if err := applyOperator(target, op, val); err != nil {
			return spec, err
		}
		if negate {
			spec.negated = append(spec.negated, *target)
		}
	}
	if len(freeWords) > 0 {
		// Always case-insensitive; a leading flag group like (?s) composes with it.
		pattern := "(?i)" + strings.Join(freeWords, " ")
		if len(pattern) > maxSearchPatternLen {
			return spec, fmt.Errorf("free-text pattern too long")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return spec, err
		}
		spec.freeText = re
		spec.freeWords = freeWords
	}
	return spec, nil
}

// tokenizeQuery splits on whitespace but keeps double-quoted runs together,
// stripping the quotes. e.g. `subject:"weekly report"` -> `subject:weekly report`.
func tokenizeQuery(raw string) []string {
	var tokens []string
	var b strings.Builder
	inQuote := false
	flush := func() {
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
			b.Reset()
		}
	}
	for _, r := range raw {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t' || r == '\n' || r == '\r') && !inQuote:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// splitOperator reports whether tok is operator-shaped (`name:value`, name a
// simple word) and returns the lowercased name and raw value.
func splitOperator(tok string) (op, val string, ok bool) {
	i := strings.Index(tok, ":")
	if i <= 0 {
		return "", "", false
	}
	op = strings.ToLower(tok[:i])
	if strings.ContainsAny(op, " \t") {
		return "", "", false
	}
	return op, tok[i+1:], true
}

// matchesSpec ANDs every present condition. Free-text still ORs across
// subject/body/addresses via matchesRE.
func matchesSpec(email *cache.CachedEmail, spec searchSpec) bool {
	if spec.isEmpty() || email.Envelope == nil {
		return false
	}
	for _, v := range spec.from {
		if !anyAddressContains(email.Env().From, v) {
			return false
		}
	}
	for _, v := range spec.to {
		if !anyAddressContains(email.Env().To, v) &&
			!anyAddressContains(email.Env().Cc, v) &&
			!anyAddressContains(email.Env().Bcc, v) {
			return false
		}
	}
	for _, v := range spec.subject {
		if !containsFold(email.Env().Subject, v) {
			return false
		}
	}
	for _, v := range spec.label {
		if !anyLabelContains(email.Labels, v) {
			return false
		}
	}
	if spec.hasAttachment != nil {
		if (len(email.Attachments) > 0) != *spec.hasAttachment {
			return false
		}
	}
	if spec.unread != nil {
		// unread == true requires the absence of \Seen.
		if (*spec.unread) == hasSeenFlag(email.Flags) {
			return false
		}
	}
	if spec.olderThan != nil || spec.newerThan != nil {
		age := time.Since(email.Env().Date)
		if spec.olderThan != nil && age <= *spec.olderThan {
			return false
		}
		if spec.newerThan != nil && age >= *spec.newerThan {
			return false
		}
	}
	if spec.freeText != nil && !matchesRE(email, spec.freeText) {
		return false
	}
	for _, n := range spec.negated {
		if matchesSpec(email, n) {
			return false
		}
	}
	return true
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func anyAddressContains(addrs []*cache.Address, v string) bool {
	for _, a := range addrs {
		if a == nil {
			continue
		}
		if containsFold(a.PersonalName, v) ||
			containsFold(a.MailboxName, v) ||
			containsFold(a.HostName, v) ||
			containsFold(a.MailboxName+"@"+a.HostName, v) {
			return true
		}
	}
	return false
}

func anyLabelContains(labels []string, v string) bool {
	for _, l := range labels {
		if containsFold(l, v) {
			return true
		}
	}
	return false
}
