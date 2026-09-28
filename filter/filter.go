package filter

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"github.com/golang-mail/go-mail/cache"
)

// bodyTagRE strips HTML tags from email bodies when evaluating "body" conditions.
// Hoisted so the regex isn't recompiled per email per filter.
var bodyTagRE = regexp.MustCompile(`<[^>]*>`)

// matchRegexCache memoizes regexes compiled for the "matches" operator. Filter
// evaluation runs over thousands of emails with identical patterns; without
// this cache, regexp.Compile is called once per email per matching condition.
var matchRegexCache sync.Map // map[string]matchRegexEntry

type matchRegexEntry struct {
	re  *regexp.Regexp
	err error
}

// compileMatchRegex returns the cached (?i)-prefixed regex for the given
// pattern, compiling it on first use. Errors are cached too so invalid
// patterns don't trigger repeated compile attempts.
func compileMatchRegex(pattern string) (*regexp.Regexp, error) {
	if v, ok := matchRegexCache.Load(pattern); ok {
		entry := v.(matchRegexEntry)
		return entry.re, entry.err
	}
	re, err := regexp.Compile("(?i)" + pattern)
	matchRegexCache.Store(pattern, matchRegexEntry{re: re, err: err})
	return re, err
}

// MatchMailbox checks if a mailbox name matches a filter pattern.
// Patterns can use glob wildcards (e.g. "gitlab.*" matches "gitlab.bots").
func MatchMailbox(pattern, mailbox string) bool {
	if strings.ContainsAny(pattern, "*?[") {
		matched, err := filepath.Match(pattern, mailbox)
		return err == nil && matched
	}
	return pattern == mailbox
}

type Node struct {
	Filter *Filter
	Edges  []*Node
	key    string // graph key, unique per filter position
	index  int    // position in the source filter list
}

// Specificity returns a score indicating how specific a filter is.
// Higher score = more specific = should run first.
// This ensures that narrow filters (e.g. "from equals x AND subject contains y")
// are evaluated before broad ones (e.g. "from contains @domain").
func Specificity(f *Filter) int {
	score := 0

	// More conditions = more specific
	score += len(f.Conditions) * 10

	// AND conditions are stricter than OR
	if strings.ToLower(f.ConditionsType) == "and" || f.ConditionsType == "" {
		score += 5
	}

	// Exact operators are more specific than partial matches
	for _, c := range f.Conditions {
		switch strings.ToLower(c.Operator) {
		case "equals":
			score += 4
		case "begins_with", "ends_with":
			score += 2
		case "contains":
			score += 1
		case "not-contains":
			score += 1
		case "exists":
			score += 3
		}
		// Narrower fields are more specific
		switch strings.ToLower(c.Field) {
		case "from", "to", "cc", "bcc", "sender":
			score += 2
		case "from/to":
			score += 1
		case "from/to/cc/bcc":
			score += 0
		case "subject":
			score += 2
		case "body":
			score += 1
		}
	}

	// Scoped to specific mailboxes = more specific
	if len(f.Mailboxes) > 0 {
		score += 3
	}

	// Scoped to specific accounts = more specific
	if len(f.Accounts) > 0 {
		score += 2
	}

	return score
}

// BuildDependencyGraph keys nodes by position, not name: names may be empty
// or duplicated in filters.yml and must never collapse two filters into one.
func BuildDependencyGraph(filters []Filter) map[string]*Node {
	nodes := make(map[string]*Node, len(filters))
	ordered := make([]*Node, len(filters))
	for i := range filters {
		n := &Node{Filter: &filters[i], key: strconv.Itoa(i), index: i}
		nodes[n.key] = n
		ordered[i] = n
	}

	for _, node := range ordered {
		for _, action := range node.Filter.Actions {
			if !strings.EqualFold(action.Type, "move") {
				continue
			}
			for _, other := range ordered {
				if other == node {
					continue
				}
				if filterReadsMailbox(other.Filter, action.Value) {
					node.Edges = append(node.Edges, other)
				}
			}
		}
	}
	return nodes
}

// filterReadsMailbox reports whether a move into mailbox lands in f's scope.
func filterReadsMailbox(f *Filter, mailbox string) bool {
	patterns := f.Mailboxes
	if len(patterns) == 0 {
		patterns = []string{"INBOX"}
	}
	for _, p := range patterns {
		if MatchMailbox(p, mailbox) {
			return true
		}
	}
	return false
}

const (
	white = 0 // unvisited
	gray  = 1 // visiting
	black = 2 // visited
)

func TopologicalSort(nodes map[string]*Node) ([]*Filter, bool) {
	// Deterministic traversal in config order; map iteration is random.
	ordered := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		ordered = append(ordered, n)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].index < ordered[j].index })

	var sortedNodes []*Node
	colors := make(map[string]int, len(nodes))
	path := make(map[string]*Node) // parent map to reconstruct cycle

	var cycleStart, cycleEnd *Node

	var dfs func(node *Node) bool
	dfs = func(node *Node) bool {
		colors[node.key] = gray

		for _, edge := range node.Edges {
			if colors[edge.key] == gray {
				cycleStart = edge
				cycleEnd = node
				path[edge.key] = node
				return false // cycle detected
			}
			if colors[edge.key] == white {
				path[edge.key] = node
				if !dfs(edge) {
					return false
				}
			}
		}
		colors[node.key] = black
		sortedNodes = append(sortedNodes, node)
		return true
	}

	for _, n := range ordered {
		if colors[n.key] != white {
			continue
		}
		if !dfs(n) {
			slog.Error("cycle detected in filter dependencies")
			var cycle []string
			curr := cycleEnd
			for curr != nil {
				cycle = append([]string{curr.Filter.Name}, cycle...)
				if curr == cycleStart {
					break
				}
				curr = path[curr.key]
			}
			cycle = append([]string{cycleStart.Filter.Name}, cycle...)
			slog.Error("cycle path", "path", strings.Join(cycle, " -> "))
			return nil, false
		}
	}

	// The result from DFS is in reverse topological order
	for i, j := 0, len(sortedNodes)-1; i < j; i, j = i+1, j-1 {
		sortedNodes[i], sortedNodes[j] = sortedNodes[j], sortedNodes[i]
	}

	// Push depths forward: a consumer sits one level below its deepest producer.
	depth := make(map[string]int, len(sortedNodes))
	for _, n := range sortedNodes {
		for _, edge := range n.Edges {
			if d := depth[n.key] + 1; d > depth[edge.key] {
				depth[edge.key] = d
			}
		}
	}

	// Stable sort: preserve topological order (by depth), but within the
	// same depth level, defer "run last" plugin filters so cleanup filters
	// (move/delete) thin out the mailbox before side-effecting plugins fire.
	// Among filters in the same tier, more specific filters come first.
	sort.SliceStable(sortedNodes, func(i, j int) bool {
		di, dj := depth[sortedNodes[i].key], depth[sortedNodes[j].key]
		if di != dj {
			return di < dj
		}
		li, lj := FilterRunsLast(sortedNodes[i].Filter), FilterRunsLast(sortedNodes[j].Filter)
		if li != lj {
			return !li
		}
		return Specificity(sortedNodes[i].Filter) > Specificity(sortedNodes[j].Filter)
	})

	sortedFilters := make([]*Filter, len(sortedNodes))
	for i, n := range sortedNodes {
		sortedFilters[i] = n.Filter
	}
	return sortedFilters, true
}

type Filter struct {
	ID             string      `yaml:"id,omitempty" json:"id,omitempty"`
	Name           string      `yaml:"name"`
	Accounts       []string    `yaml:"accounts"`
	Mailboxes      []string    `yaml:"mailboxes"`
	ConditionsType string      `yaml:"conditions_type"`
	Conditions     []Condition `yaml:"conditions"`
	Actions        []Action    `yaml:"actions"`
	// Disabled filters stay in filters.yml but never run.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	// ActiveFrom/ActiveUntil bound the run window (RFC 3339); Sweep clears them once passed.
	ActiveFrom  string `yaml:"active_from,omitempty" json:"active_from,omitempty"`
	ActiveUntil string `yaml:"active_until,omitempty" json:"active_until,omitempty"`
	// OnExpire is what Sweep does at ActiveUntil: "disable" (default) or "remove".
	OnExpire string `yaml:"on_expire,omitempty" json:"on_expire,omitempty"`
}

type Condition struct {
	Field    string `yaml:"field"`
	Operator string `yaml:"operator"`
	Value    string `yaml:"value"`
}

type Action struct {
	Type  string `yaml:"type"`
	Value string `yaml:"value"`
}

type Config struct {
	Filters []Filter `yaml:"filters"`
}

// PluginVerdict is the subset of plugin.EvaluateResponse the filter package needs.
// Defined here to avoid a cycle between filter and plugin packages.
type PluginVerdict struct {
	Matched bool
	Actions []Action
	Reason  string
}

// PluginEvalContext carries the account/mailbox the email was evaluated in.
// Populated when the caller knows them (cache-based filter pipeline); zero values
// otherwise (e.g. live IMAP-based matching paths).
type PluginEvalContext struct {
	Account string
	Mailbox string
	// Target is the live cached email when matching runs on a snapshot;
	// run bookkeeping must land there. Nil means the matched email is live.
	Target *cache.CachedEmail
}

// PluginEvaluator is set by main.go at startup to wire in the plugin manager.
// When non-nil, filter conditions of the form `field: plugin:<name>` will be
// dispatched through this function. Returning (nil, nil) means "no match".
var PluginEvaluator func(name string, email *cache.CachedEmail, filterConfig string, evalCtx PluginEvalContext) (*PluginVerdict, error)

// PluginRunsLast is set by main.go to report whether a plugin's filters should
// be deferred until after non-plugin filters within the same dependency level.
// Defaults to true when not wired or when the plugin's manifest does not set
// the field — see plugin.Manifest.PrefersRunLast.
var PluginRunsLast func(name string) bool

// Compiled holds the deterministic, per-account output of preparing a filter
// pipeline. Build once per (filters.yml content × account) and reuse across
// filter runs.
type Compiled struct {
	// Sorted is the filter list in execution order (topologically sorted with
	// specificity and run-last tiebreaks already applied).
	Sorted []*Filter
	// WildcardMoveTargets is the set of mailboxes that some wildcard-scoped
	// filter moves messages into; filterAccount uses it to prevent re-applying
	// the same wildcard filter on its own output.
	WildcardMoveTargets map[string]bool
	// RequiredMailboxes is the set of mailboxes referenced by any move action;
	// filterAccount ensures they exist on the IMAP server.
	RequiredMailboxes map[string]bool
}

// reorderConditionsPluginsLast moves every plugin: condition to the end of
// f.Conditions, preserving the relative order within each group. Mutates the
// filter in place. Safe because filters in the compile cache are owned by
// the cache and re-parsed on every filters.yml change.
func reorderConditionsPluginsLast(f *Filter) {
	if len(f.Conditions) < 2 {
		return
	}
	hasPlugin := false
	for _, c := range f.Conditions {
		if strings.HasPrefix(strings.ToLower(c.Field), "plugin:") {
			hasPlugin = true
			break
		}
	}
	if !hasPlugin {
		return
	}
	reordered := make([]Condition, 0, len(f.Conditions))
	var deferred []Condition
	for _, c := range f.Conditions {
		if strings.HasPrefix(strings.ToLower(c.Field), "plugin:") {
			deferred = append(deferred, c)
		} else {
			reordered = append(reordered, c)
		}
	}
	reordered = append(reordered, deferred...)
	f.Conditions = reordered
}

// CompileForAccount produces the per-account filter pipeline. Pure function:
// callers can safely cache the result keyed by (cfg content × accountName).
// Returns ok=false when the filters form a cycle.
func CompileForAccount(cfg Config, accountName string) (*Compiled, bool) {
	var accountFilters []Filter
	for _, f := range cfg.Filters {
		if len(f.Accounts) == 0 {
			accountFilters = append(accountFilters, f)
			continue
		}
		for _, acc := range f.Accounts {
			if acc == accountName {
				accountFilters = append(accountFilters, f)
				break
			}
		}
	}

	nodes := BuildDependencyGraph(accountFilters)
	sorted, ok := TopologicalSort(nodes)
	if !ok {
		return nil, false
	}

	// Reorder each filter's conditions so plugin: conditions evaluate last.
	// Doing this here means matchesCachedVerbose doesn't have to allocate
	// two slices per email per filter. Plugins can have side effects
	// (e.g. posting to Slack), so checking cheap pure conditions first lets
	// AND short-circuit before we trigger them.
	for _, f := range sorted {
		reorderConditionsPluginsLast(f)
	}

	wildcardMoveTargets := make(map[string]bool)
	for _, f := range sorted {
		hasWildcard := false
		for _, fm := range f.Mailboxes {
			if strings.ContainsAny(fm, "*?[") {
				hasWildcard = true
				break
			}
		}
		if hasWildcard {
			for _, action := range f.Actions {
				if strings.ToLower(action.Type) == "move" && action.Value != "" {
					wildcardMoveTargets[action.Value] = true
				}
			}
		}
	}

	requiredMailboxes := make(map[string]bool)
	for _, f := range accountFilters {
		for _, action := range f.Actions {
			if strings.ToLower(action.Type) == "move" && action.Value != "" {
				requiredMailboxes[action.Value] = true
			}
		}
	}

	return &Compiled{
		Sorted:              sorted,
		WildcardMoveTargets: wildcardMoveTargets,
		RequiredMailboxes:   requiredMailboxes,
	}, true
}

// FilterRunsLast reports whether the filter should be deferred to the "last"
// tier within its dependency level. True when any plugin: condition references
// a plugin whose manifest opts into RunLast (the default).
func FilterRunsLast(f *Filter) bool {
	if PluginRunsLast == nil {
		return false
	}
	for _, c := range f.Conditions {
		field := strings.ToLower(c.Field)
		if !strings.HasPrefix(field, "plugin:") {
			continue
		}
		name := c.Field[7:]
		if PluginRunsLast(name) {
			return true
		}
	}
	return false
}

func formatCachedAddress(addr *cache.Address) string {
	return fmt.Sprintf("%s <%s@%s>", addr.PersonalName, addr.MailboxName, addr.HostName)
}

func checkCachedAddresses(addrs []*cache.Address, cond Condition) bool {
	for _, addr := range addrs {
		email := addr.MailboxName + "@" + addr.HostName
		if checkString(email, cond) {
			return true
		}
		if addr.PersonalName != "" && checkString(addr.PersonalName, cond) {
			return true
		}
		if addr.PersonalName != "" && checkString(formatCachedAddress(addr), cond) {
			return true
		}
	}
	return false
}

// MatchesCachedVerbose is like MatchesCached but logs every condition check at Info level.
func MatchesCachedVerbose(email *cache.CachedEmail, f Filter) bool {
	matched, _ := matchesCachedVerbose(email, f, true, PluginEvalContext{})
	return matched
}

// MatchesCachedWithActions is like MatchesCached, but also returns any extra actions
// contributed by plugin conditions in the filter. These actions should be appended
// to the filter's own f.Actions when the filter matches.
// evalCtx provides account/mailbox info forwarded to plugin evaluators.
func MatchesCachedWithActions(email *cache.CachedEmail, f Filter, evalCtx PluginEvalContext) (bool, []Action) {
	return matchesCachedVerbose(email, f, false, evalCtx)
}

func matchesCachedVerbose(email *cache.CachedEmail, f Filter, verbose bool, evalCtx PluginEvalContext) (bool, []Action) {
	var pluginActions []Action
	subject := ""
	if email.Envelope != nil {
		subject = email.Envelope.Subject
	}

	if email.Envelope == nil {
		if verbose {
			slog.Info("[filter-debug] skip: no envelope", "filter", f.Name, "subject", subject)
		}
		return false, nil
	}
	if len(f.Conditions) == 0 {
		if verbose {
			slog.Info("[filter-debug] match: no conditions (matches all)", "filter", f.Name, "subject", subject)
		}
		return true, nil
	}

	conditionsType := strings.ToLower(f.ConditionsType)
	if conditionsType == "" {
		conditionsType = "and"
	}

	if verbose {
		slog.Info("[filter-debug] checking filter", "filter", f.Name, "conditions_type", conditionsType, "conditions", len(f.Conditions), "subject", subject)
	}

	// Conditions are pre-reordered (plugin: conditions moved last) by
	// CompileForAccount, so we can iterate f.Conditions directly here.
	for _, cond := range f.Conditions {
		match := false
		actualValue := ""
		field := strings.ToLower(cond.Field)
		if strings.HasPrefix(field, "header:") {
			headerName := cond.Field[7:]
			if email.Headers != nil {
				if strings.ToLower(cond.Operator) == "exists" {
					for k := range email.Headers {
						if strings.EqualFold(k, headerName) {
							match = true
							actualValue = "(exists)"
							break
						}
					}
				} else {
					for k, v := range email.Headers {
						if strings.EqualFold(k, headerName) {
							match = checkString(v, cond)
							actualValue = v
							break
						}
					}
				}
				if actualValue == "" {
					actualValue = "(header not found)"
				}
			} else {
				actualValue = "(no headers)"
			}
		} else if strings.HasPrefix(field, "plugin:") {
			pluginName := cond.Field[7:]
			if PluginEvaluator == nil {
				actualValue = "(no plugin evaluator)"
			} else {
				verdict, err := PluginEvaluator(pluginName, email, cond.Value, evalCtx)
				if err != nil {
					slog.Error("plugin evaluation failed", "plugin", pluginName, "filter", f.Name, "error", err)
					actualValue = "(error: " + err.Error() + ")"
				} else if verdict != nil {
					match = verdict.Matched
					if match && len(verdict.Actions) > 0 {
						pluginActions = append(pluginActions, verdict.Actions...)
					}
					if verdict.Reason != "" {
						actualValue = verdict.Reason
					} else if match {
						actualValue = "(plugin matched)"
					} else {
						actualValue = "(plugin no match)"
					}
				} else {
					actualValue = "(plugin nil verdict)"
				}
			}
		} else {
			switch field {
			case "from":
				match = checkCachedAddresses(email.Envelope.From, cond)
				actualValue = formatCachedAddressList(email.Envelope.From)
			case "sender":
				senders := email.Envelope.Sender
				if len(senders) == 0 {
					senders = email.Envelope.From
				}
				match = checkCachedAddresses(senders, cond)
				actualValue = formatCachedAddressList(senders)
			case "to":
				match = checkCachedAddresses(email.Envelope.To, cond)
				actualValue = formatCachedAddressList(email.Envelope.To)
			case "cc":
				match = checkCachedAddresses(email.Envelope.Cc, cond)
				actualValue = formatCachedAddressList(email.Envelope.Cc)
			case "bcc":
				match = checkCachedAddresses(email.Envelope.Bcc, cond)
				actualValue = formatCachedAddressList(email.Envelope.Bcc)
			case "from/to/cc/bcc":
				match = checkCachedAddresses(email.Envelope.From, cond) ||
					checkCachedAddresses(email.Envelope.To, cond) ||
					checkCachedAddresses(email.Envelope.Cc, cond) ||
					checkCachedAddresses(email.Envelope.Bcc, cond)
				actualValue = "from=" + formatCachedAddressList(email.Envelope.From) + " to=" + formatCachedAddressList(email.Envelope.To)
			case "from/to":
				match = checkCachedAddresses(email.Envelope.From, cond) ||
					checkCachedAddresses(email.Envelope.To, cond)
				actualValue = "from=" + formatCachedAddressList(email.Envelope.From) + " to=" + formatCachedAddressList(email.Envelope.To)
			case "subject":
				match = checkString(email.Envelope.Subject, cond)
				actualValue = email.Envelope.Subject
			case "age":
				if strings.ToLower(cond.Operator) == "older_than" {
					duration, err := ParseDuration(cond.Value)
					if err != nil {
						slog.Error("invalid duration for age condition", "value", cond.Value)
						continue
					}
					if time.Since(email.Envelope.Date) > duration {
						match = true
					}
					actualValue = fmt.Sprintf("age=%s", time.Since(email.Envelope.Date).Round(time.Second))
				}
			case "body":
				plainBody := bodyTagRE.ReplaceAllString(email.Body, " ")
				match = checkString(plainBody, cond)
				if len(plainBody) > 100 {
					actualValue = plainBody[:100] + "..."
				} else {
					actualValue = plainBody
				}
			case "status":
				var hasSeen bool
				for _, flag := range email.Flags {
					if strings.EqualFold(flag, "\\Seen") {
						hasSeen = true
						break
					}
				}
				if strings.ToLower(cond.Value) == "read" && hasSeen {
					match = true
				} else if strings.ToLower(cond.Value) == "unread" && !hasSeen {
					match = true
				}
				if hasSeen {
					actualValue = "read"
				} else {
					actualValue = "unread"
				}
			case "label":
				has := false
				for _, l := range email.Labels {
					if strings.EqualFold(l, cond.Value) {
						has = true
						break
					}
				}
				switch strings.ToLower(cond.Operator) {
				case "contains", "equals":
					match = has
				case "not-contains":
					match = !has
				}
				if len(email.Labels) == 0 {
					actualValue = "(no labels)"
				} else {
					actualValue = strings.Join(email.Labels, ", ")
				}
			default:
				// Unknown fields fail the condition; skipping would let an
				// all-typo `and` filter match everything.
				if verbose {
					slog.Info("[filter-debug]   condition: unknown field", "filter", f.Name, "field", cond.Field)
				}
				match = false
				actualValue = "(unknown field)"
			}
		}

		if verbose {
			slog.Info("[filter-debug]   condition",
				"filter", f.Name,
				"field", cond.Field,
				"operator", cond.Operator,
				"expected", cond.Value,
				"actual", actualValue,
				"match", match,
			)
		}

		if conditionsType == "or" && match {
			if verbose {
				slog.Info("[filter-debug] result: MATCH (OR: at least one condition matched)", "filter", f.Name, "subject", subject)
			}
			return true, pluginActions
		}
		if conditionsType == "and" && !match {
			if verbose {
				slog.Info("[filter-debug] result: NO MATCH (AND: condition failed)", "filter", f.Name, "subject", subject)
			}
			return false, nil
		}
	}

	result := conditionsType != "or"
	if verbose {
		if result {
			slog.Info("[filter-debug] result: MATCH (all conditions passed)", "filter", f.Name, "subject", subject)
		} else {
			slog.Info("[filter-debug] result: NO MATCH (OR: no condition matched)", "filter", f.Name, "subject", subject)
		}
	}
	if !result {
		return false, nil
	}
	return true, pluginActions
}

func formatCachedAddressList(addrs []*cache.Address) string {
	if len(addrs) == 0 {
		return "(empty)"
	}
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = formatCachedAddress(a)
	}
	return strings.Join(parts, ", ")
}

// ParseDuration parses durations like "30d", "2w", "6m", "1y" (m=months, y=years).
func ParseDuration(durationStr string) (time.Duration, error) {
	durationStr = strings.ToLower(strings.TrimSpace(durationStr))
	if len(durationStr) < 2 {
		return 0, fmt.Errorf("invalid duration string: %s", durationStr)
	}
	unit := durationStr[len(durationStr)-1:]
	valueStr := durationStr[:len(durationStr)-1]
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return 0, err
	}

	var unitDuration time.Duration
	switch unit {
	case "d":
		unitDuration = 24 * time.Hour
	case "w":
		unitDuration = 7 * 24 * time.Hour
	case "m":
		// Approximation, assuming 30 days per month
		unitDuration = 30 * 24 * time.Hour
	case "y":
		// Approximation, assuming 365 days per year
		unitDuration = 365 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("unknown duration unit: %s", unit)
	}

	return time.Duration(value) * unitDuration, nil
}

func checkString(s string, cond Condition) bool {
	s = strings.ToLower(s)
	val := strings.ToLower(cond.Value)
	switch strings.ToLower(cond.Operator) {
	case "contains":
		return strings.Contains(s, val)
	case "not-contains":
		return !strings.Contains(s, val)
	case "equals":
		return s == val
	case "begins_with":
		return strings.HasPrefix(s, val)
	case "ends_with":
		return strings.HasSuffix(s, val)
	case "matches":
		re, err := compileMatchRegex(cond.Value)
		if err != nil {
			slog.Error("invalid regex in filter condition", "value", cond.Value, "error", err)
			return false
		}
		return re.MatchString(s)
	}
	return false
}

func PerformAction(c *client.Client, uidSet *imap.SeqSet, action Action) {
	switch strings.ToLower(action.Type) {
	case "move":
		if action.Value == "" {
			slog.Warn("move action requires a destination mailbox")
			return
		}
		mailboxName := action.Value
		slog.Debug("moving messages", "mailbox", mailboxName)
		if err := c.UidMove(uidSet, mailboxName); err != nil {
			slog.Error("failed to move messages", "error", err)
		}
	case "delete":
		slog.Debug("marking messages for deletion")
		item := imap.FormatFlagsOp(imap.AddFlags, true)
		flags := []interface{}{imap.DeletedFlag}
		if err := c.UidStore(uidSet, item, flags, nil); err != nil {
			slog.Error("failed to mark messages for deletion", "error", err)
		}
	case "permanent_delete":
		slog.Debug("permanently deleting messages")
		item := imap.FormatFlagsOp(imap.AddFlags, true)
		flags := []interface{}{imap.DeletedFlag}
		if err := c.UidStore(uidSet, item, flags, nil); err != nil {
			slog.Error("failed to mark messages for permanent deletion", "error", err)
		}
		if err := c.Expunge(nil); err != nil {
			slog.Error("failed to expunge permanently deleted messages", "error", err)
		}
	case "mark_as_read":
		slog.Debug("marking messages as read")
		item := imap.FormatFlagsOp(imap.AddFlags, true)
		flags := []interface{}{imap.SeenFlag}
		if err := c.UidStore(uidSet, item, flags, nil); err != nil {
			slog.Error("failed to mark messages as read", "error", err)
		}
	case "mark_as_unread":
		slog.Debug("marking messages as unread")
		item := imap.FormatFlagsOp(imap.RemoveFlags, true)
		flags := []interface{}{imap.SeenFlag}
		if err := c.UidStore(uidSet, item, flags, nil); err != nil {
			slog.Error("failed to mark messages as unread", "error", err)
		}
	case "flag":
		if action.Value == "" {
			slog.Warn("flag action requires a flag value")
			return
		}
		slog.Debug("adding flag to messages", "flag", action.Value)
		item := imap.FormatFlagsOp(imap.AddFlags, true)
		flags := []interface{}{action.Value}
		if err := c.UidStore(uidSet, item, flags, nil); err != nil {
			slog.Error("failed to flag messages", "error", err)
		}
	case "mark_as_spam":
		// Handled at cache level — no IMAP action needed.
		slog.Debug("marking messages as spam (bayesian)")
	default:
		slog.Error("unknown filter action type", "type", action.Type)
	}
}
