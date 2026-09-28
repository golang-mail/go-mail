package plugin

import (
	"encoding/json"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

// Envelope is the wire-format wrapper for every message exchanged with a plugin.
type Envelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"` // "request" | "response" | "event"
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// InitRequest is sent once after the plugin process starts.
type InitRequest struct {
	HostVersion string          `json:"host_version"`
	Config      json.RawMessage `json:"config,omitempty"`
}

// Manifest is what a plugin returns from its init response.
type Manifest struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Actions   []string `json:"actions,omitempty"`
	Wants     []string `json:"wants,omitempty"`     // "envelope" | "headers" | "body"
	Cacheable bool     `json:"cacheable,omitempty"` // verdicts may be cached per (uid, plugin_version)
	// RunLast defers filters that reference this plugin until after all non-plugin
	// filters have run, so cleanup filters (move/delete) can clear noise before a
	// side-effecting plugin (auto-reply, notify) is invoked.
	// Pointer so plugins that don't set the field still default to true; set to
	// false explicitly to opt out (pure-observer plugins with no side effects).
	RunLast *bool `json:"run_last,omitempty"`
	// Rerunnable disables the host's "run once per email" guard. By default the
	// host records each plugin invocation in CachedEmail.PluginsRun and skips
	// future evaluations, which prevents duplicate side effects (Slack reposts,
	// auto-reply storms). Set true for pure-observer plugins that should re-run
	// whenever the email is re-filtered (e.g. label/tag fetchers that need to
	// pick up upstream label changes).
	Rerunnable bool `json:"rerunnable,omitempty"`
	// RescanInterval, when set, asks the host to periodically re-evaluate all
	// filters referencing this plugin against ALL emails in those filters'
	// mailbox scope (processAll=true). Used by observer plugins that need to
	// pick up upstream changes (e.g. label fetchers that should refresh when a
	// GitLab issue gets a new label after the email was first processed).
	// Format: a Go duration string ("1h", "30m"). Empty disables rescanning.
	// Only meaningful for plugins that also set Rerunnable=true.
	RescanInterval string `json:"rescan_interval,omitempty"`
	// WantsBatchComplete asks the host to send a batch_complete event after
	// every filter run. Use this to drive digest plugins (one Slack post per
	// run instead of one per email) without managing your own timers.
	WantsBatchComplete bool `json:"wants_batch_complete,omitempty"`
	// WantsMailboxState extends WantsBatchComplete: the batch_complete payload
	// includes a snapshot of every email in the touched mailboxes. Body and
	// headers honor the existing Wants opt-in. Larger payload — only set if
	// the plugin actually needs the full mailbox view.
	WantsMailboxState bool `json:"wants_mailbox_state,omitempty"`
	// HostMethods enumerates the host.* methods this plugin is allowed to call
	// (e.g. "list_mailboxes", "list_emails", "get_email"). Calls to methods
	// not on this list are rejected with an "error: method not granted"
	// response. Empty = no host RPC access. Names are without the "host." prefix.
	HostMethods []string `json:"host_methods,omitempty"`
	// WantsSchedule asks the host to emit scheduled_tick events at the cadence
	// given by Schedule. Independent of evaluate/batch_complete — a plugin can
	// opt into ticks only, or combine them with the per-email flow.
	WantsSchedule bool `json:"wants_schedule,omitempty"`
	// Schedule controls the cadence of scheduled_tick events. Accepts either
	// a Go duration string ("10m", "1h") for fixed intervals, or a cron
	// expression (standard 5-field or @daily/@hourly/@every shortcuts) for
	// wall-clock anchored runs. Empty disables ticks even when WantsSchedule
	// is true. Distinct from RescanInterval: RescanInterval re-runs evaluate
	// against existing emails; Schedule only fires the event so the plugin
	// can pull state via host RPC and decide what to do.
	Schedule string `json:"schedule,omitempty"`
}

// AllowsHostMethod reports whether the manifest's HostMethods grants the given
// method name. The method argument may include or omit the "host." prefix.
func (m *Manifest) AllowsHostMethod(method string) bool {
	if m == nil {
		return false
	}
	name := method
	if len(name) > 5 && name[:5] == "host." {
		name = name[5:]
	}
	for _, g := range m.HostMethods {
		if g == name || g == method {
			return true
		}
	}
	return false
}

// PrefersRunLast reports whether filters using this plugin should run after
// non-plugin filters. Defaults to true when unset.
func (m *Manifest) PrefersRunLast() bool {
	if m == nil || m.RunLast == nil {
		return true
	}
	return *m.RunLast
}

// IsRerunnable reports whether the host should skip the run-once guard for
// this plugin. Defaults to false.
func (m *Manifest) IsRerunnable() bool {
	return m != nil && m.Rerunnable
}

// EvalEmail is the email payload sent to a plugin.
// Body / Headers are omitted unless the plugin's manifest declared a need for them.
type EvalEmail struct {
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

// EvaluateRequest is sent to ask the plugin to evaluate one email.
type EvaluateRequest struct {
	Email        *EvalEmail      `json:"email"`
	FilterConfig string          `json:"filter_config,omitempty"` // value of the plugin: condition
	PluginConfig json.RawMessage `json:"plugin_config,omitempty"` // opaque per-plugin config
}

// PluginAction is a single action returned by a plugin.
type PluginAction struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

// EvaluateResponse is what the plugin returns from evaluate.
// All fields are optional. An empty {} payload means "no match, no actions".
type EvaluateResponse struct {
	Matched bool           `json:"matched,omitempty"`
	Actions []PluginAction `json:"actions,omitempty"`
	Reason  string         `json:"reason,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// MethodHostLog is the host.* method a plugin calls to ship a structured,
// leveled log line to the host's logger. Unlike data methods it needs no
// HostMethods grant and isn't rate-limited: observability must work for every
// plugin, including external/dialed ones that have no stderr path to the host.
const MethodHostLog = "host.log"

// HostLogRequest is the payload for host.log. Level is one of
// debug|info|warn|error (defaults to info when empty/unknown). Fields carries
// optional structured attributes rendered alongside the message.
type HostLogRequest struct {
	Level  string            `json:"level,omitempty"`
	Msg    string            `json:"msg"`
	Fields map[string]string `json:"fields,omitempty"`
}

// MethodBatchComplete is the envelope method name for the post-filter-run event.
const MethodBatchComplete = "batch_complete"

// MailboxRef identifies one account+mailbox pair touched during a filter run.
type MailboxRef struct {
	Account string `json:"account"`
	Mailbox string `json:"mailbox"`
}

// MailboxStateEntry is a snapshot of one mailbox at the end of a filter run.
// Emails carry envelope/headers/body subject to the plugin's Wants opt-in.
type MailboxStateEntry struct {
	Account string       `json:"account"`
	Mailbox string       `json:"mailbox"`
	Emails  []*EvalEmail `json:"emails"`
}

// BatchCompleteCounts summarizes work done during a filter run.
type BatchCompleteCounts struct {
	Matched   int `json:"matched"`
	Processed int `json:"processed"`
}

// BatchCompleteEvent is the payload sent (one per receiving plugin) after a
// filter run finishes. Plugins that want it must declare WantsBatchComplete
// in their manifest. MailboxState is populated only for plugins that also
// declared WantsMailboxState.
type BatchCompleteEvent struct {
	RunID        string              `json:"run_id"`
	StartedAt    time.Time           `json:"started_at"`
	EndedAt      time.Time           `json:"ended_at"`
	Trigger      string              `json:"trigger"` // "startup" | "periodic" | "watcher" | "manual" | "rescan"
	Mailboxes    []MailboxRef        `json:"mailboxes,omitempty"`
	Counts       BatchCompleteCounts `json:"counts"`
	MailboxState []MailboxStateEntry `json:"mailbox_state,omitempty"`
}

// MethodScheduledTick is the envelope method name for the schedule event.
const MethodScheduledTick = "scheduled_tick"

// ScheduledTickEvent is sent on each fire of a plugin's Schedule. Plugins use
// it to decide whether to pull state via host RPC and emit a digest/summary.
// Since carries the previous fire timestamp (zero on the first tick) so the
// plugin can compute "what changed since last time" without keeping its own
// clock state. Force is set when the host fires the tick on operator demand
// (UI "Run Now") — plugins should bypass any change-detection gating so the
// operator can validate the side-effecting path end-to-end.
type ScheduledTickEvent struct {
	FiredAt  time.Time `json:"fired_at"`
	Schedule string    `json:"schedule"`
	Since    time.Time `json:"since,omitempty"`
	Force    bool      `json:"force,omitempty"`
}
