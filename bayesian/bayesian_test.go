package bayesian

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func spamMsg(i int) Message {
	return Message{
		Subject: fmt.Sprintf("WIN money now %d", i),
		Body:    "click here for free prize lottery casino",
		From:    fmt.Sprintf("promo%d@lottery-wins.example", i),
	}
}

func hamMsg(i int) Message {
	return Message{
		Subject: fmt.Sprintf("Weekly report %d", i),
		Body:    "attached is the sprint summary and meeting notes",
		From:    "lead@corp.example",
	}
}

// trained returns a classifier past the readiness gate.
func trained(t *testing.T) *Classifier {
	c := New(filepath.Join(t.TempDir(), "model.yml"))
	for i := range MinTraining {
		c.Train(spamMsg(i), true)
		c.Train(hamMsg(i), false)
	}
	return c
}

func TestClassifySeparatesSpamFromHam(t *testing.T) {
	c := trained(t)
	if !c.IsTrained() {
		t.Fatal("classifier past the gate must report trained")
	}
	spam := c.Classify(Message{Subject: "free prize", Body: "click here casino lottery"})
	ham := c.Classify(Message{Subject: "report", Body: "sprint summary meeting notes"})
	if spam < 0.9 {
		t.Fatalf("spam score %.3f should be decisive", spam)
	}
	if ham > 0.1 {
		t.Fatalf("ham score %.3f should be decisive", ham)
	}
}

// A heavily ham-skewed model must not treat unknown words as spam evidence.
func TestNovelWordsStayNeutralUnderImbalance(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "model.yml"))
	for i := range MinTraining {
		c.Train(spamMsg(i), true)
	}
	for i := range 2000 {
		c.Train(Message{
			Subject: fmt.Sprintf("Weekly report %d", i),
			Body:    fmt.Sprintf("summary of sprint %d with notes item%d topic%d", i, i, i*7),
			From:    "lead@corp.example",
		}, false)
	}
	score := c.Classify(Message{Subject: "Rechnung Oktober", Body: "anbei die unterlagen zur besprechung nächste woche"})
	if math.Abs(score-0.5) > 0.05 {
		t.Fatalf("all-novel message scored %.3f, want ~0.5", score)
	}
}

func TestSenderDomainIsEvidence(t *testing.T) {
	c := trained(t)
	// Body words are unknown; only the sender domain carries signal.
	spam := c.Classify(Message{Subject: "hello", Body: "quarterly numbers attached", From: "x@lottery-wins.example"})
	ham := c.Classify(Message{Subject: "hello", Body: "quarterly numbers attached", From: "x@corp.example"})
	if spam <= ham {
		t.Fatalf("known spam domain %.3f should outscore known ham domain %.3f", spam, ham)
	}
}

func TestHeaderFeatures(t *testing.T) {
	m := Message{
		Subject: "Hi",
		Body:    "x",
		From:    "News Team <news@list.example>",
		ReplyTo: "bounce@other.example",
		Headers: map[string]string{
			"List-Unsubscribe":       "<mailto:u@list.example>",
			"Authentication-Results": "mx; dkim=pass header.d=list.example; spf=fail; dmarc=none",
			"X-Marketoid":            "abc",
			"Content-Type":           "text/html; charset=utf-8",
		},
	}
	got := map[string]bool{}
	for _, tok := range tokenize(m) {
		got[tok] = true
	}
	for _, want := range []string{
		"from:list.example", "from:news@list.example", "fromname:news",
		"replyto:other.example", "replyto-mismatch",
		"hdr:list-unsubscribe", "hdr:x-marketoid",
		"auth:dkim=pass", "auth:spf=fail", "auth:dmarc=none",
		"ctype:text/html",
		"subj:hi",
	} {
		if !got[want] {
			t.Errorf("missing token %q in %v", want, tokenize(m))
		}
	}
}

func TestReadinessGate(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "model.yml"))
	c.Train(spamMsg(0), true)
	c.Train(hamMsg(0), false)
	if c.IsTrained() {
		t.Fatal("one example per class must not be enough to auto-move mail")
	}
	if got := c.Classify(spamMsg(0)); got != 0.5 {
		t.Fatalf("classify before readiness = %.3f, want neutral 0.5", got)
	}
	st := c.GetStats()
	if st.Ready || st.MinTraining != MinTraining {
		t.Fatalf("stats = %+v", st)
	}
}

func TestUntrainReversesTraining(t *testing.T) {
	c := trained(t)
	before := c.GetStats()
	c.Untrain(spamMsg(0), true)
	after := c.GetStats()
	if after.SpamCount != before.SpamCount-1 {
		t.Fatalf("spam count %d -> %d, want one fewer", before.SpamCount, after.SpamCount)
	}
	// Untraining below zero must clamp.
	e := New(filepath.Join(t.TempDir(), "m.yml"))
	e.Untrain(spamMsg(0), true)
	if e.GetStats().SpamCount != 0 {
		t.Fatal("untrain on empty model must clamp at zero")
	}
}

func TestSafeList(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "model.yml"))
	if !c.AddSafe("boss@corp.example") || c.AddSafe("boss@corp.example") {
		t.Fatal("AddSafe should report whether the entry was new")
	}
	c.AddSafe("trusted.example")
	tests := map[string]bool{
		"boss@corp.example":   true,
		"BOSS@CORP.EXAMPLE":   true,
		"other@corp.example":  false,
		"any@trusted.example": true,
		"any@evil.example":    false,
	}
	for addr, want := range tests {
		if got := c.IsSafe(addr); got != want {
			t.Errorf("IsSafe(%q) = %v want %v", addr, got, want)
		}
	}
	if !c.RemoveSafe("trusted.example") || c.IsSafe("any@trusted.example") {
		t.Fatal("RemoveSafe should drop the domain entry")
	}
	if got := c.GetSafeList(); len(got) != 1 {
		t.Fatalf("safe list = %v", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.yml")
	c := New(path)
	c.Train(spamMsg(0), true)
	c.Train(hamMsg(0), false)
	c.AddSafe("a@b.example")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded := New(path)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if reloaded.GetStats() != c.GetStats() || !reloaded.IsSafe("a@b.example") {
		t.Fatalf("reloaded state differs: %+v vs %+v", reloaded.GetStats(), c.GetStats())
	}
}

// Save is skipped while nothing changed, so a filter run does not rewrite the file.
func TestSaveOnlyWhenDirty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.yml")
	c := New(path)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("clean classifier must not write a file")
	}
	c.Train(spamMsg(0), true)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "sentinel" {
		t.Fatalf("second save without changes rewrote the file (was %d bytes)", info.Size())
	}
}

// Occurrence-count models from the previous engine are discarded, not misread.
func TestLoadDiscardsOldFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.yml")
	old := "spam_tokens:\n  free: 40\nham_tokens:\n  report: 900\nspam_count: 3\nham_count: 300\nspam_total_tokens: 40\nham_total_tokens: 900\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New(path)
	if err := c.Load(); err != nil {
		t.Fatal(err)
	}
	if st := c.GetStats(); st.SpamCount != 0 || st.HamCount != 0 {
		t.Fatalf("old-format model should load as empty, got %+v", st)
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "absent.yml"))
	if err := c.Load(); err != nil {
		t.Fatalf("loading a missing model should start empty, got %v", err)
	}
	if c.IsTrained() {
		t.Fatal("empty model must not be trained")
	}
}

func TestChiSquareTail(t *testing.T) {
	if got := chi2Q(0, 2); math.Abs(got-1) > 1e-9 {
		t.Fatalf("chi2Q(0,2) = %v, want 1", got)
	}
	if got := chi2Q(1000, 4); got > 1e-6 {
		t.Fatalf("chi2Q(1000,4) = %v, want ~0", got)
	}
	// Median of chi-square with 2 degrees of freedom is 2 ln 2.
	if got := chi2Q(2*math.Ln2, 2); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("chi2Q(2ln2,2) = %v, want 0.5", got)
	}
}
