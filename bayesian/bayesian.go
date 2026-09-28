// Package bayesian scores mail with the Graham/Robinson token model combined
// through Fisher's chi-square test, the approach used by SpamBayes and
// bogofilter. Unlike textbook Naive Bayes it is insensitive to the spam/ham
// ratio of the training set, and words it has never seen carry no evidence.
package bayesian

import (
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/golang-mail/go-mail/cache"
	"gopkg.in/yaml.v3"
)

// MinTraining is the number of examples each class needs before the
// classifier reports itself trained and scores are acted upon.
const MinTraining = 20

const (
	modelVersion = 2

	maxClues        = 150  // strongest tokens considered per message
	minStrength     = 0.1  // |p-0.5| below this is noise, not evidence
	unknownStrength = 0.45 // weight of the 0.5 prior for rarely seen tokens
	unknownProb     = 0.5
	maxBodyTokens   = 5000
	minWordLen      = 2
	maxWordLen      = 40
)

// Message is the classifier's view of one email.
type Message struct {
	Subject string
	Body    string
	From    string // "Name <addr>" or bare address
	ReplyTo string
	Headers map[string]string
}

// Classifier holds per-token message counts for spam and ham.
type Classifier struct {
	mu sync.RWMutex

	Version int `yaml:"model_version"`

	// Number of spam / ham messages each token occurred in.
	SpamTokens map[string]int `yaml:"spam_tokens"`
	HamTokens  map[string]int `yaml:"ham_tokens"`

	SpamCount int `yaml:"spam_count"`
	HamCount  int `yaml:"ham_count"`

	// SafeList holds addresses ("user@host") or domains never classified as spam.
	SafeList []string `yaml:"safe_list,omitempty"`

	path  string `yaml:"-"`
	dirty bool   `yaml:"-"`
}

// Stats describes the model for the API.
type Stats struct {
	SpamCount      int  `json:"spamCount"`
	HamCount       int  `json:"hamCount"`
	SpamVocabSize  int  `json:"spamVocabSize"`
	HamVocabSize   int  `json:"hamVocabSize"`
	TotalVocabSize int  `json:"totalVocabSize"`
	Ready          bool `json:"ready"`
	MinTraining    int  `json:"minTraining"`
}

// New creates an empty classifier persisted at path.
func New(path string) *Classifier {
	return &Classifier{
		Version:    modelVersion,
		SpamTokens: make(map[string]int),
		HamTokens:  make(map[string]int),
		path:       path,
	}
}

// Load reads the model from disk. A missing file or a model written by the
// previous occurrence-count engine yields an empty model; the safe list survives.
func (c *Classifier) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var loaded Classifier
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		return err
	}
	c.SafeList = loaded.SafeList
	if loaded.Version != modelVersion {
		c.SpamTokens = make(map[string]int)
		c.HamTokens = make(map[string]int)
		c.SpamCount, c.HamCount = 0, 0
		c.dirty = true
		return nil
	}
	c.SpamTokens = loaded.SpamTokens
	c.HamTokens = loaded.HamTokens
	if c.SpamTokens == nil {
		c.SpamTokens = make(map[string]int)
	}
	if c.HamTokens == nil {
		c.HamTokens = make(map[string]int)
	}
	c.SpamCount = loaded.SpamCount
	c.HamCount = loaded.HamCount
	return nil
}

// Save writes the model when something changed since the last save.
func (c *Classifier) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := cache.WriteFileAtomic(c.path, data, 0o600); err != nil {
		return err
	}
	c.dirty = false
	return nil
}

// Train records m as spam or ham.
func (c *Classifier) Train(m Message, spam bool) {
	tokens := uniqueTokens(m)
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := c.HamTokens
	if spam {
		counts = c.SpamTokens
		c.SpamCount++
	} else {
		c.HamCount++
	}
	for t := range tokens {
		counts[t]++
	}
	c.dirty = true
}

// Untrain reverses an earlier Train of the same message and class.
func (c *Classifier) Untrain(m Message, spam bool) {
	tokens := uniqueTokens(m)
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := c.HamTokens
	if spam {
		counts = c.SpamTokens
		if c.SpamCount > 0 {
			c.SpamCount--
		}
	} else if c.HamCount > 0 {
		c.HamCount--
	}
	for t := range tokens {
		if counts[t] > 1 {
			counts[t]--
		} else {
			delete(counts, t)
		}
	}
	c.dirty = true
}

// Classify returns the spam probability of m in [0, 1]. Neutral 0.5 is
// returned until the model is trained or when no token carries evidence.
func (c *Classifier) Classify(m Message) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.ready() {
		return 0.5
	}

	nSpam := float64(c.SpamCount)
	nHam := float64(c.HamCount)
	clues := make([]float64, 0, 64)
	for t := range uniqueTokens(m) {
		sc := float64(c.SpamTokens[t])
		hc := float64(c.HamTokens[t])
		n := sc + hc
		if n == 0 {
			continue
		}
		b := sc / nSpam
		g := hc / nHam
		p := b / (b + g)
		// Robinson's smoothing pulls rare tokens toward the 0.5 prior.
		f := (unknownStrength*unknownProb + n*p) / (unknownStrength + n)
		if math.Abs(f-0.5) < minStrength {
			continue
		}
		clues = append(clues, f)
	}
	if len(clues) == 0 {
		return 0.5
	}
	sort.Slice(clues, func(i, j int) bool {
		return math.Abs(clues[i]-0.5) > math.Abs(clues[j]-0.5)
	})
	if len(clues) > maxClues {
		clues = clues[:maxClues]
	}

	// Fisher's method on both tails; their disagreement is the verdict.
	var lnSpam, lnHam float64
	for _, f := range clues {
		f = math.Min(math.Max(f, 1e-6), 1-1e-6)
		lnHam += math.Log(f)
		lnSpam += math.Log(1 - f)
	}
	df := 2 * len(clues)
	s := 1 - chi2Q(-2*lnSpam, df)
	h := 1 - chi2Q(-2*lnHam, df)
	return (s - h + 1) / 2
}

// chi2Q is the upper tail of the chi-square distribution for even degrees
// of freedom, which is all Fisher's method needs.
func chi2Q(x2 float64, df int) float64 {
	m := x2 / 2
	term := math.Exp(-m)
	sum := term
	for i := 1; i < df/2; i++ {
		term *= m / float64(i)
		sum += term
	}
	return math.Min(sum, 1)
}

// GetStats returns model statistics.
func (c *Classifier) GetStats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	total := len(c.SpamTokens)
	for t := range c.HamTokens {
		if _, ok := c.SpamTokens[t]; !ok {
			total++
		}
	}
	return Stats{
		SpamCount:      c.SpamCount,
		HamCount:       c.HamCount,
		SpamVocabSize:  len(c.SpamTokens),
		HamVocabSize:   len(c.HamTokens),
		TotalVocabSize: total,
		Ready:          c.ready(),
		MinTraining:    MinTraining,
	}
}

// IsTrained reports whether both classes have enough examples to act on scores.
func (c *Classifier) IsTrained() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ready()
}

func (c *Classifier) ready() bool {
	return c.SpamCount >= MinTraining && c.HamCount >= MinTraining
}

// IsSafe reports whether the address or its domain is on the safe list.
func (c *Classifier) IsSafe(email string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	email = strings.ToLower(strings.TrimSpace(email))
	domain := email
	if idx := strings.LastIndex(email, "@"); idx >= 0 {
		domain = email[idx+1:]
	}
	for _, entry := range c.SafeList {
		e := strings.ToLower(entry)
		if strings.Contains(e, "@") {
			if e == email {
				return true
			}
		} else if e == domain {
			return true
		}
	}
	return false
}

// AddSafe adds an address or domain; false when it was already present.
func (c *Classifier) AddSafe(entry string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry = strings.ToLower(strings.TrimSpace(entry))
	if entry == "" {
		return false
	}
	for _, e := range c.SafeList {
		if strings.ToLower(e) == entry {
			return false
		}
	}
	c.SafeList = append(c.SafeList, entry)
	c.dirty = true
	return true
}

// RemoveSafe removes an address or domain; false when it was not present.
func (c *Classifier) RemoveSafe(entry string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry = strings.ToLower(strings.TrimSpace(entry))
	for i, e := range c.SafeList {
		if strings.ToLower(e) == entry {
			c.SafeList = append(c.SafeList[:i], c.SafeList[i+1:]...)
			c.dirty = true
			return true
		}
	}
	return false
}

// GetSafeList returns a copy of the safe list.
func (c *Classifier) GetSafeList() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]string, len(c.SafeList))
	copy(result, c.SafeList)
	return result
}

var (
	htmlTagRe = regexp.MustCompile(`<[^>]*>`)
	authRe    = regexp.MustCompile(`(?i)\b(spf|dkim|dmarc)=([a-z]+)`)
)

// Headers whose mere presence is a feature, beyond the X-* family.
var presenceHeaders = map[string]bool{
	"list-unsubscribe": true,
	"list-id":          true,
	"precedence":       true,
	"auto-submitted":   true,
	"feedback-id":      true,
}

func uniqueTokens(m Message) map[string]struct{} {
	set := make(map[string]struct{}, 256)
	for _, t := range tokenize(m) {
		set[t] = struct{}{}
	}
	return set
}

// tokenize turns a message into feature strings: body words, prefixed
// subject words, sender and reply-to features, and header-derived signals.
func tokenize(m Message) []string {
	tokens := make([]string, 0, 256)

	for _, w := range words(m.Subject) {
		tokens = append(tokens, "subj:"+w)
	}
	body := words(htmlTagRe.ReplaceAllString(m.Body, " "))
	if len(body) > maxBodyTokens {
		body = body[:maxBodyTokens]
	}
	tokens = append(tokens, body...)

	fromName, fromAddr := splitAddress(m.From)
	fromDomain := domainOf(fromAddr)
	if fromAddr != "" {
		tokens = append(tokens, "from:"+fromAddr)
	}
	if fromDomain != "" {
		tokens = append(tokens, "from:"+fromDomain)
	}
	for _, w := range words(fromName) {
		tokens = append(tokens, "fromname:"+w)
	}
	if _, replyAddr := splitAddress(m.ReplyTo); replyAddr != "" {
		replyDomain := domainOf(replyAddr)
		if replyDomain != "" {
			tokens = append(tokens, "replyto:"+replyDomain)
			if fromDomain != "" && replyDomain != fromDomain {
				tokens = append(tokens, "replyto-mismatch")
			}
		}
	}

	for k, v := range m.Headers {
		lk := strings.ToLower(k)
		switch {
		case strings.HasPrefix(lk, "x-") || presenceHeaders[lk]:
			tokens = append(tokens, "hdr:"+lk)
		}
		switch lk {
		case "authentication-results", "arc-authentication-results":
			for _, mm := range authRe.FindAllStringSubmatch(v, -1) {
				tokens = append(tokens, "auth:"+strings.ToLower(mm[1])+"="+strings.ToLower(mm[2]))
			}
		case "content-type":
			if ct, _, _ := strings.Cut(strings.ToLower(v), ";"); ct != "" {
				tokens = append(tokens, "ctype:"+strings.TrimSpace(ct))
			}
		case "precedence":
			tokens = append(tokens, "prec:"+strings.ToLower(strings.TrimSpace(v)))
		case "x-mailer", "user-agent":
			if w := words(v); len(w) > 0 {
				tokens = append(tokens, "mailer:"+w[0])
			}
		}
	}
	return tokens
}

func words(text string) []string {
	text = strings.ToLower(text)
	var out []string
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) < minWordLen || len(w) > maxWordLen {
			continue
		}
		out = append(out, w)
	}
	return out
}

// splitAddress separates "Name <addr>" into its lowercase parts.
func splitAddress(s string) (name, addr string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if i := strings.LastIndex(s, "<"); i >= 0 {
		name = strings.Trim(strings.TrimSpace(s[:i]), `"`)
		addr = strings.TrimSuffix(s[i+1:], ">")
	} else {
		addr = s
	}
	return name, strings.ToLower(strings.TrimSpace(addr))
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return ""
}
