package server

import (
	"testing"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

func addr(personal, mailbox, host string) *cache.Address {
	return &cache.Address{PersonalName: personal, MailboxName: mailbox, HostName: host}
}

// sample email used across matchesSpec tests.
func sampleEmail() *cache.CachedEmail {
	return &cache.CachedEmail{
		Envelope: &cache.Envelope{
			Subject: "Weekly report draft",
			From:    []*cache.Address{addr("Alice Smith", "alice", "example.com")},
			To:      []*cache.Address{addr("Bob Jones", "bob", "example.com")},
			Cc:      []*cache.Address{addr("Carol", "carol", "other.org")},
		},
		Body:        "Here is the body text about budgets.",
		Flags:       []string{"\\Seen"},
		Attachments: []string{"report.pdf"},
		Labels:      []string{"Work", "Important"},
	}
}

func TestParseQueryExtractsOperators(t *testing.T) {
	spec, err := parseQuery("from:alice to:bob subject:report label:Work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.from) != 1 || spec.from[0] != "alice" {
		t.Errorf("from = %v, want [alice]", spec.from)
	}
	if len(spec.to) != 1 || spec.to[0] != "bob" {
		t.Errorf("to = %v, want [bob]", spec.to)
	}
	if len(spec.subject) != 1 || spec.subject[0] != "report" {
		t.Errorf("subject = %v, want [report]", spec.subject)
	}
	if len(spec.label) != 1 || spec.label[0] != "Work" {
		t.Errorf("label = %v, want [Work]", spec.label)
	}
	if spec.freeText != nil {
		t.Errorf("freeText = %v, want nil", spec.freeText)
	}
}

func TestParseQueryQuotedSubject(t *testing.T) {
	spec, err := parseQuery(`subject:"weekly report"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.subject) != 1 || spec.subject[0] != "weekly report" {
		t.Errorf("subject = %v, want [weekly report]", spec.subject)
	}
}

func TestParseQueryHasAndIs(t *testing.T) {
	spec, err := parseQuery("has:attachment is:unread")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.hasAttachment == nil || !*spec.hasAttachment {
		t.Errorf("hasAttachment = %v, want true", spec.hasAttachment)
	}
	if spec.unread == nil || !*spec.unread {
		t.Errorf("unread = %v, want true", spec.unread)
	}

	spec2, _ := parseQuery("is:read")
	if spec2.unread == nil || *spec2.unread {
		t.Errorf("unread = %v, want false (read)", spec2.unread)
	}
}

func TestParseQueryBareWordsAreFreeText(t *testing.T) {
	spec, err := parseQuery("urgent from:bob report")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.from) != 1 || spec.from[0] != "bob" {
		t.Errorf("from = %v, want [bob]", spec.from)
	}
	if spec.freeText == nil {
		t.Fatal("freeText = nil, want compiled regex")
	}
	// Bare words are joined into the existing single regex (case-insensitive),
	// preserving prior whole-query behavior.
	if !spec.freeText.MatchString("an URGENT REPORT today") {
		t.Errorf("freeText should match joined bare words case-insensitively")
	}
	if spec.freeText.MatchString("urgent only") {
		t.Errorf("joined regex requires both words adjacent, should not match 'urgent only'")
	}
}

func TestParseQueryUnknownOperatorFallsToFreeText(t *testing.T) {
	spec, err := parseQuery("foo:bar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.freeText == nil {
		t.Fatal("freeText = nil, want regex for unknown operator token")
	}
	if !spec.freeText.MatchString("foo:bar") {
		t.Errorf("freeText should match the literal unknown token")
	}
}

func TestParseQueryEmptyIsEmptySpec(t *testing.T) {
	spec, err := parseQuery("   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !spec.isEmpty() {
		t.Errorf("expected empty spec for blank query")
	}
}

func TestMatchesSpecFrom(t *testing.T) {
	e := sampleEmail()
	spec, _ := parseQuery("from:alice")
	if !matchesSpec(e, spec) {
		t.Errorf("from:alice should match")
	}
	spec, _ = parseQuery("from:nobody")
	if matchesSpec(e, spec) {
		t.Errorf("from:nobody should not match")
	}
	// Matches host and full email too.
	spec, _ = parseQuery("from:example.com")
	if !matchesSpec(e, spec) {
		t.Errorf("from:example.com should match host")
	}
}

func TestMatchesSpecToIncludesCcBcc(t *testing.T) {
	e := sampleEmail()
	spec, _ := parseQuery("to:bob")
	if !matchesSpec(e, spec) {
		t.Errorf("to:bob should match To")
	}
	spec, _ = parseQuery("to:carol")
	if !matchesSpec(e, spec) {
		t.Errorf("to:carol should match Cc")
	}
}

func TestMatchesSpecSubject(t *testing.T) {
	e := sampleEmail()
	spec, _ := parseQuery(`subject:"weekly report"`)
	if !matchesSpec(e, spec) {
		t.Errorf("subject phrase should match (case-insensitive substring)")
	}
	spec, _ = parseQuery("subject:invoice")
	if matchesSpec(e, spec) {
		t.Errorf("subject:invoice should not match")
	}
}

func TestMatchesSpecLabel(t *testing.T) {
	e := sampleEmail()
	spec, _ := parseQuery("label:work")
	if !matchesSpec(e, spec) {
		t.Errorf("label:work should match case-insensitively")
	}
	spec, _ = parseQuery("label:spam")
	if matchesSpec(e, spec) {
		t.Errorf("label:spam should not match")
	}
}

func TestMatchesSpecHasAttachment(t *testing.T) {
	e := sampleEmail()
	spec, _ := parseQuery("has:attachment")
	if !matchesSpec(e, spec) {
		t.Errorf("has:attachment should match email with attachment")
	}
	e.Attachments = nil
	if matchesSpec(e, spec) {
		t.Errorf("has:attachment should not match email without attachment")
	}
}

func TestMatchesSpecIsReadUnread(t *testing.T) {
	e := sampleEmail() // has \Seen
	spec, _ := parseQuery("is:read")
	if !matchesSpec(e, spec) {
		t.Errorf("is:read should match seen email")
	}
	spec, _ = parseQuery("is:unread")
	if matchesSpec(e, spec) {
		t.Errorf("is:unread should not match seen email")
	}
	e.Flags = nil
	if !matchesSpec(e, spec) {
		t.Errorf("is:unread should match email without \\Seen")
	}
}

func TestMatchesSpecAndCombination(t *testing.T) {
	e := sampleEmail()
	spec, _ := parseQuery("from:alice subject:report")
	if !matchesSpec(e, spec) {
		t.Errorf("from:alice AND subject:report should match")
	}
	spec, _ = parseQuery("from:alice subject:invoice")
	if matchesSpec(e, spec) {
		t.Errorf("from:alice AND subject:invoice should not match (AND)")
	}
}

func TestMatchesSpecFreeTextPreserved(t *testing.T) {
	e := sampleEmail()
	spec, _ := parseQuery("budgets")
	if !matchesSpec(e, spec) {
		t.Errorf("free text should match body")
	}
	// mixed free text + operator: both must hold.
	spec, _ = parseQuery("budgets from:alice")
	if !matchesSpec(e, spec) {
		t.Errorf("free text + matching operator should match")
	}
	spec, _ = parseQuery("budgets from:nobody")
	if matchesSpec(e, spec) {
		t.Errorf("free text matches but operator fails -> overall no match")
	}
}

func TestMatchesSpecEmptyReturnsFalse(t *testing.T) {
	e := sampleEmail()
	spec, _ := parseQuery("")
	if matchesSpec(e, spec) {
		t.Errorf("empty spec should match nothing")
	}
}

func TestParseQueryTimeOperators(t *testing.T) {
	spec, err := parseQuery("older_than:30d newer_than:2w")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.olderThan == nil || *spec.olderThan != 30*24*time.Hour {
		t.Errorf("olderThan = %v, want 30d", spec.olderThan)
	}
	if spec.newerThan == nil || *spec.newerThan != 2*7*24*time.Hour {
		t.Errorf("newerThan = %v, want 2w", spec.newerThan)
	}
}

func TestParseQueryInvalidDurationIsAnError(t *testing.T) {
	if _, err := parseQuery("older_than:soon"); err == nil {
		t.Fatal("an unparseable duration on a known operator must be rejected")
	}
}

func TestMatchesSpecOlderThan(t *testing.T) {
	e := sampleEmail()
	e.Envelope.Date = time.Now().Add(-10 * 24 * time.Hour) // 10 days old
	spec, _ := parseQuery("older_than:7d")
	if !matchesSpec(e, spec) {
		t.Errorf("10-day-old email should match older_than:7d")
	}
	spec, _ = parseQuery("older_than:30d")
	if matchesSpec(e, spec) {
		t.Errorf("10-day-old email should not match older_than:30d")
	}
}

func TestMatchesSpecNewerThan(t *testing.T) {
	e := sampleEmail()
	e.Envelope.Date = time.Now().Add(-3 * 24 * time.Hour) // 3 days old
	spec, _ := parseQuery("newer_than:7d")
	if !matchesSpec(e, spec) {
		t.Errorf("3-day-old email should match newer_than:7d")
	}
	spec, _ = parseQuery("newer_than:1d")
	if matchesSpec(e, spec) {
		t.Errorf("3-day-old email should not match newer_than:1d")
	}
}
