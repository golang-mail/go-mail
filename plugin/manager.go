package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

// MailboxStateProvider returns a snapshot of the given mailboxes for inclusion
// in a batch_complete payload. Implementations should honor the plugin's wants
// list (envelope / headers / body) to control how much per-email data is
// serialized. Called once per plugin that declared WantsMailboxState=true.
type MailboxStateProvider func(mailboxes []MailboxRef, wants []string) []MailboxStateEntry

// Config is the on-disk shape of plugins.yml.
type Config struct {
	Plugins []Spec `yaml:"plugins"`
}

// LoadConfig reads plugins.yml. Returns (nil, nil) if the file does not exist.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// SaveConfig writes cfg to path atomically (write-temp-then-rename) so a
// crash mid-write can never leave a half-serialized plugins.yml on disk.
// Permissions match the existing file when present; default 0o644 for new
// files. The temp file lives in the same directory as path so the final
// rename is atomic (cross-filesystem renames would degrade to copy+delete).
func SaveConfig(path string, cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal plugins config: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".plugins.yml.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below fails before rename.
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Mirror the existing file's mode bits when present so an editor can keep
	// its umask preference (e.g. 0o600 for files with secrets).
	if info, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmpPath, info.Mode().Perm())
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	tmpPath = "" // suppress the defer cleanup; rename succeeded
	return nil
}

// Manager owns the set of plugin clients + a verdict cache for cacheable plugins.
type Manager struct {
	hostVersion string

	// applyMu serializes lifecycle operations (Init, ApplyConfig, RestartPlugin,
	// Shutdown) so concurrent fsnotify fires and UI mutations don't race on the
	// underlying spec/client maps.
	applyMu sync.Mutex

	mu sync.RWMutex
	// specs is the source of truth for what plugins SHOULD exist, including
	// entries where Disabled=true. clients only holds live (running or recently
	// crashed) child processes; a disabled spec lives in specs but not clients.
	specs   map[string]Spec
	clients map[string]*Client

	cacheMu sync.Mutex
	verdict map[verdictKey]*EvaluateResponse

	stateMu       sync.Mutex
	stateProvider MailboxStateProvider

	hostMu       sync.Mutex
	api          HostAPI
	rateLimiters map[string]*tokenBucket

	schedMu     sync.Mutex
	cron        *cron.Cron
	cronEntries map[string]cron.EntryID
}

// SetMailboxStateProvider installs the function used to populate the
// mailbox_state field of batch_complete events for plugins that opt in via
// WantsMailboxState. Without a provider the field is left empty.
func (m *Manager) SetMailboxStateProvider(p MailboxStateProvider) {
	m.stateMu.Lock()
	m.stateProvider = p
	m.stateMu.Unlock()
}

// NewManager builds an empty manager. Call Init to spawn plugins.
func NewManager(hostVersion string) *Manager {
	return &Manager{
		hostVersion:  hostVersion,
		specs:        make(map[string]Spec),
		clients:      make(map[string]*Client),
		verdict:      make(map[verdictKey]*EvaluateResponse),
		rateLimiters: make(map[string]*tokenBucket),
		cronEntries:  make(map[string]cron.EntryID),
	}
}

// SetHostAPI installs the read-only data surface plugins can call through
// host.* RPC. Without this, all host.* requests fail with "host API not
// configured". Safe to call before or after Init — already-running clients
// get the dispatcher refreshed in place.
func (m *Manager) SetHostAPI(api HostAPI) {
	m.hostMu.Lock()
	m.api = api
	m.hostMu.Unlock()

	m.mu.RLock()
	for _, c := range m.clients {
		c.SetHostHandler(m.handlerFor(c))
	}
	m.mu.RUnlock()
}

// handlerFor builds the per-client host-RPC dispatcher, sharing one
// rate-limit bucket per plugin name across reconnects.
func (m *Manager) handlerFor(c *Client) HostHandler {
	name := c.spec.Name
	qps := c.spec.HostCallQPS
	m.hostMu.Lock()
	bucket, ok := m.rateLimiters[name]
	if !ok {
		bucket = newTokenBucket(qps)
		m.rateLimiters[name] = bucket
	}
	m.hostMu.Unlock()
	return &pluginHostDispatcher{mgr: m, pluginName: name, limiter: bucket}
}

// Init spawns all configured plugins. Returns an error if any plugin fails to start;
// the caller is expected to treat that as fatal so a misconfigured plugin doesn't get
// silently skipped at runtime. Disabled specs are remembered but not spawned.
func (m *Manager) Init(cfg *Config) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	if cfg == nil {
		return nil
	}
	if dup := duplicateName(cfg); dup != "" {
		return fmt.Errorf("duplicate plugin name %q in config", dup)
	}
	for _, spec := range cfg.Plugins {
		if spec.Name == "" {
			slog.Warn("skipping plugin with empty name")
			continue
		}
		m.mu.Lock()
		m.specs[spec.Name] = spec
		m.mu.Unlock()
		if spec.Disabled {
			slog.Info("plugin disabled in config, not spawning", "name", spec.Name)
			continue
		}
		if err := m.spawnLocked(spec); err != nil {
			return fmt.Errorf("plugin %q: %w", spec.Name, err)
		}
	}
	return nil
}

// spawnLocked creates a Client for spec, starts it (init handshake), registers
// the host handler and (if applicable) the schedule. Caller must hold applyMu.
// On success the new client is in m.clients; on failure it is not (the partial
// client object — if any — is discarded by Start's own cleanup).
func (m *Manager) spawnLocked(spec Spec) error {
	c := NewClient(spec, m.hostVersion)
	c.SetHostHandler(m.handlerFor(c))
	if err := c.Start(); err != nil {
		return err
	}
	m.mu.Lock()
	m.clients[spec.Name] = c
	m.mu.Unlock()
	m.schedMu.Lock()
	m.registerScheduleLocked(c)
	m.schedMu.Unlock()
	return nil
}

// teardownLocked is the inverse of spawnLocked: pulls the schedule, shuts down
// the client, and removes it from clients. Caller must hold applyMu. Safe to
// call when the plugin isn't currently running (no-op).
func (m *Manager) teardownLocked(name string) {
	m.schedMu.Lock()
	m.removeScheduleLocked(name)
	m.schedMu.Unlock()

	m.mu.Lock()
	c := m.clients[name]
	delete(m.clients, name)
	m.mu.Unlock()
	if c != nil {
		c.Shutdown()
	}
}

// Get returns the client for a plugin name, or nil.
func (m *Manager) Get(name string) *Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[name]
}

// GetSpec returns the operator-set spec for a plugin name. Includes disabled
// entries (still in the spec set). The second return is false when the name
// isn't known to the manager.
func (m *Manager) GetSpec(name string) (Spec, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.specs[name]
	return s, ok
}

// Specs returns the current spec set as a Config snapshot. Used by HTTP
// handlers when they want to construct a payload that reflects what the
// manager believes is configured (which may differ briefly from disk during
// an in-flight ApplyConfig). For source-of-truth, callers should LoadConfig
// the YAML file directly.
func (m *Manager) Specs() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := &Config{Plugins: make([]Spec, 0, len(m.specs))}
	names := make([]string, 0, len(m.specs))
	for n := range m.specs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out.Plugins = append(out.Plugins, m.specs[n])
	}
	return out
}

// ManifestOf returns the manifest reported by the named plugin at init time,
// or nil if the plugin is not running (disabled, never started, or crashed).
func (m *Manager) ManifestOf(name string) *Manifest {
	c := m.Get(name)
	if c == nil {
		return nil
	}
	return c.Manifest()
}

// Has reports whether a plugin with the given name is registered.
func (m *Manager) Has(name string) bool {
	return m.Get(name) != nil
}

// Names returns the registered plugin names.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.clients))
	for n := range m.clients {
		out = append(out, n)
	}
	return out
}

// Statuses returns a snapshot of all configured plugin states (for /api/plugins).
// Operator-disabled specs surface as Running=false, Disabled=true entries so
// the UI can render them — Disabled here covers both operator and supervisor
// off-states; the UI distinguishes via LastError (supervisor sets it, operator
// doesn't).
func (m *Manager) Statuses() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Status, 0, len(m.specs))
	for name, spec := range m.specs {
		if c, ok := m.clients[name]; ok {
			out = append(out, c.Status())
			continue
		}
		out = append(out, Status{
			Name:     name,
			Running:  false,
			Disabled: spec.Disabled,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Shutdown stops all plugins.
func (m *Manager) Shutdown() {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.StopSchedules()
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, c := range m.clients {
		slog.Info("shutting down plugin", "name", name)
		c.Shutdown()
	}
}

// RestartPlugin tears down and respawns the child for `name`. Reads the spec
// from the current in-memory set so the new child reflects any recent edits
// (e.g. an operator-edited config block). No-op if the plugin is operator-
// disabled. Errors from spawn propagate so the caller can surface them.
func (m *Manager) RestartPlugin(name string) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.RLock()
	spec, ok := m.specs[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("plugin %q not configured", name)
	}
	m.teardownLocked(name)
	if spec.Disabled {
		slog.Info("plugin disabled, not restarting", "name", name)
		return nil
	}
	slog.Info("restarting plugin", "name", name)
	return m.spawnLocked(spec)
}

// ApplyConfig brings the live plugin set in line with cfg: added entries are
// spawned, removed entries are torn down, modified entries are respawned with
// the new spec. Unchanged entries are left running. Idempotent — calling with
// the current in-memory config is a no-op.
//
// Returns nil on success; per-plugin spawn errors are logged but do not abort
// the rest of the diff, so a single misconfigured plugin doesn't block other
// updates.
func (m *Manager) ApplyConfig(cfg *Config) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	if dup := duplicateName(cfg); dup != "" {
		return fmt.Errorf("duplicate plugin name %q in config", dup)
	}

	desired := make(map[string]Spec)
	if cfg != nil {
		for _, s := range cfg.Plugins {
			if s.Name == "" {
				slog.Warn("ApplyConfig: skipping plugin with empty name")
				continue
			}
			desired[s.Name] = s
		}
	}

	m.mu.RLock()
	current := make(map[string]Spec, len(m.specs))
	for k, v := range m.specs {
		current[k] = v
	}
	m.mu.RUnlock()

	added, removed, modified := diffSpecs(current, desired)

	for _, name := range removed {
		slog.Info("ApplyConfig: removing plugin", "name", name)
		m.teardownLocked(name)
		m.mu.Lock()
		delete(m.specs, name)
		m.mu.Unlock()
	}
	for _, name := range modified {
		spec := desired[name]
		slog.Info("ApplyConfig: restarting plugin", "name", name, "disabled", spec.Disabled)
		m.teardownLocked(name)
		m.mu.Lock()
		m.specs[name] = spec
		m.mu.Unlock()
		if !spec.Disabled {
			if err := m.spawnLocked(spec); err != nil {
				slog.Error("ApplyConfig: respawn failed", "name", name, "error", err)
			}
		}
	}
	for _, name := range added {
		spec := desired[name]
		slog.Info("ApplyConfig: adding plugin", "name", name, "disabled", spec.Disabled)
		m.mu.Lock()
		m.specs[name] = spec
		m.mu.Unlock()
		if !spec.Disabled {
			if err := m.spawnLocked(spec); err != nil {
				slog.Error("ApplyConfig: spawn failed", "name", name, "error", err)
			}
		}
	}
	return nil
}

// duplicateName returns the first plugin name appearing more than once in cfg,
// or "" if all names are unique. Empty names are ignored (spawn paths skip
// those). Duplicates would collide in the by-name specs/clients maps, silently
// orphaning the earlier child process — reject the whole config instead.
func duplicateName(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	seen := make(map[string]bool, len(cfg.Plugins))
	for _, s := range cfg.Plugins {
		if s.Name == "" {
			continue
		}
		if seen[s.Name] {
			return s.Name
		}
		seen[s.Name] = true
	}
	return ""
}

// diffSpecs splits desired vs current into the three diff buckets. Helper kept
// pure so the test exercises edge cases (rename, disable toggle, etc.) without
// spinning up a Manager.
func diffSpecs(current, desired map[string]Spec) (added, removed, modified []string) {
	for name, spec := range desired {
		cur, ok := current[name]
		if !ok {
			added = append(added, name)
			continue
		}
		if !specsEqual(cur, spec) {
			modified = append(modified, name)
		}
	}
	for name := range current {
		if _, ok := desired[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(modified)
	return
}

// specsEqual reports whether two specs would produce an identical child
// process. reflect.DeepEqual handles the Config field (typed any but in
// practice a map[string]any from YAML) correctly.
func specsEqual(a, b Spec) bool {
	return reflect.DeepEqual(a, b)
}

// StartSchedules brings up the cron scheduler and registers scheduled_tick
// jobs for every currently-running plugin whose manifest declared
// WantsSchedule=true and a non-empty Schedule. Idempotent — calling twice is
// a no-op. Safe to call after Init once manifests are known.
//
// New plugins added at runtime via ApplyConfig / RestartPlugin register their
// schedules in spawnLocked, so callers don't need to invoke StartSchedules
// again after the initial bring-up.
//
// Schedule syntax: either a Go duration string ("10m", "1h", "24h") or any
// expression robfig/cron/v3 accepts (5-field cron, @daily/@hourly/@every).
// Duration strings are translated to @every NN under the hood so a single
// cron scheduler can drive both forms.
func (m *Manager) StartSchedules() {
	m.schedMu.Lock()
	defer m.schedMu.Unlock()
	if m.cron != nil {
		return
	}
	m.cron = cron.New()
	m.cron.Start()

	m.mu.RLock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.mu.RUnlock()

	for _, c := range clients {
		m.registerScheduleLocked(c)
	}
}

// registerScheduleLocked adds c's manifest-declared schedule to the cron
// scheduler. No-op if the manifest doesn't request scheduling. Caller must
// hold schedMu. Safe to call when m.cron is nil — defers the registration
// implicitly (StartSchedules picks it up on the next call).
func (m *Manager) registerScheduleLocked(c *Client) {
	if m.cron == nil {
		return
	}
	manifest := c.Manifest()
	if manifest == nil || !manifest.WantsSchedule || strings.TrimSpace(manifest.Schedule) == "" {
		return
	}
	// Key by spec.Name (the unique plugins.yml entry), not manifest.Name.
	// Multiple specs can share a manifest name (e.g. several ai-summary
	// instances with different schedules) — keying by manifest would let only
	// the first register and silently drop the rest.
	pluginName := c.spec.Name
	if _, exists := m.cronEntries[pluginName]; exists {
		// Already registered — likely a no-op call. Don't double-register or
		// the same plugin would receive duplicate ticks.
		return
	}
	specStr := normalizeScheduleSpec(manifest.Schedule)
	client := c
	rawSchedule := manifest.Schedule
	var lastFiredMu sync.Mutex
	var lastFired time.Time

	id, err := m.cron.AddFunc(specStr, func() {
		now := time.Now()
		lastFiredMu.Lock()
		prev := lastFired
		lastFired = now
		lastFiredMu.Unlock()

		payload, err := json.Marshal(ScheduledTickEvent{
			FiredAt:  now,
			Schedule: rawSchedule,
			Since:    prev,
		})
		if err != nil {
			slog.Warn("marshal scheduled_tick payload", "plugin", pluginName, "err", err)
			return
		}
		if err := client.SendEvent(MethodScheduledTick, payload); err != nil {
			slog.Warn("send scheduled_tick", "plugin", pluginName, "err", err)
		}
	})
	if err != nil {
		slog.Warn("invalid plugin schedule, skipping", "plugin", pluginName, "schedule", rawSchedule, "err", err)
		return
	}
	m.cronEntries[pluginName] = id
	slog.Info("plugin schedule active", "plugin", pluginName, "schedule", rawSchedule)
}

// removeScheduleLocked unregisters a plugin's cron entry so a restart can
// rewire a fresh schedule (manifest may have changed). Caller must hold
// schedMu. No-op if the plugin had no schedule registered.
func (m *Manager) removeScheduleLocked(name string) {
	if m.cron == nil {
		return
	}
	if id, ok := m.cronEntries[name]; ok {
		m.cron.Remove(id)
		delete(m.cronEntries, name)
	}
}

// StopSchedules halts the scheduler. Blocks until any currently-running job
// completes so callers see a quiescent state before the manager exits.
// Idempotent.
func (m *Manager) StopSchedules() {
	m.schedMu.Lock()
	c := m.cron
	m.cron = nil
	m.schedMu.Unlock()
	if c == nil {
		return
	}
	<-c.Stop().Done()
}

// normalizeScheduleSpec converts a Go duration string into the @every form
// cron understands, so a single cron.Cron handles both inputs. Anything that
// doesn't parse as a duration passes through untouched (interpreted as cron
// syntax by cron.AddFunc).
func normalizeScheduleSpec(spec string) string {
	spec = strings.TrimSpace(spec)
	if _, err := time.ParseDuration(spec); err == nil {
		return "@every " + spec
	}
	return spec
}

type verdictKey struct {
	plugin       string
	version      string
	account      string
	mailbox      string
	uid          uint32
	filterConfig string
}

// Evaluate routes an evaluate request to a plugin, consulting the verdict cache when the plugin
// declared cacheable: true.
func (m *Manager) Evaluate(ctx context.Context, name string, req EvaluateRequest) (*EvaluateResponse, error) {
	c := m.Get(name)
	if c == nil {
		return nil, fmt.Errorf("plugin %q not registered", name)
	}

	manifest := c.Manifest()
	cacheable := manifest != nil && manifest.Cacheable && req.Email != nil
	var key verdictKey
	if cacheable {
		key = verdictKey{
			plugin:       name,
			version:      manifest.Version,
			account:      req.Email.Account,
			mailbox:      req.Email.Mailbox,
			uid:          req.Email.UID,
			filterConfig: req.FilterConfig,
		}
		m.cacheMu.Lock()
		v, ok := m.verdict[key]
		m.cacheMu.Unlock()
		if ok {
			return v, nil
		}
	}

	resp, err := c.Evaluate(ctx, req)
	if err != nil {
		return resp, err
	}
	if cacheable && resp != nil {
		m.cacheMu.Lock()
		m.verdict[key] = resp
		m.cacheMu.Unlock()
	}
	return resp, nil
}

// TriggerSchedule fires a scheduled_tick event at the named plugin right now,
// independent of its cron schedule. Used by the UI's "Run Now" affordance so
// an operator can validate a scheduled plugin without waiting for the next
// natural fire. Errors when the plugin isn't running or doesn't declare
// wants_schedule in its manifest.
func (m *Manager) TriggerSchedule(name string) error {
	c := m.Get(name)
	if c == nil {
		return fmt.Errorf("plugin %q not running", name)
	}
	manifest := c.Manifest()
	if manifest == nil || !manifest.WantsSchedule {
		return fmt.Errorf("plugin %q does not declare wants_schedule", name)
	}
	slog.Info("trigger scheduled_tick",
		"plugin", name,
		"manifest_name", manifest.Name,
		"manifest_version", manifest.Version,
		"schedule", manifest.Schedule)
	payload, err := json.Marshal(ScheduledTickEvent{
		FiredAt:  time.Now(),
		Schedule: manifest.Schedule,
		Force:    true,
	})
	if err != nil {
		return fmt.Errorf("marshal scheduled_tick payload: %w", err)
	}
	if err := c.SendEvent(MethodScheduledTick, payload); err != nil {
		return fmt.Errorf("send scheduled_tick: %w", err)
	}
	slog.Info("trigger scheduled_tick sent", "plugin", name)
	return nil
}

// InvalidateCache drops all cached verdicts. Call after filter config changes.
func (m *Manager) InvalidateCache() {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	m.verdict = make(map[verdictKey]*EvaluateResponse)
}

// BroadcastBatchComplete fans the given event out to every plugin whose
// manifest declared WantsBatchComplete=true. For plugins that also declared
// WantsMailboxState=true, the configured MailboxStateProvider populates the
// per-plugin mailbox_state slice (honoring that plugin's wants list).
//
// Delivery is fire-and-forget. Write errors are logged but do not stop the
// fan-out — one slow/disconnected plugin must not block the others.
func (m *Manager) BroadcastBatchComplete(event BatchCompleteEvent) {
	m.stateMu.Lock()
	provider := m.stateProvider
	m.stateMu.Unlock()

	m.mu.RLock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.mu.RUnlock()

	for _, c := range clients {
		manifest := c.Manifest()
		if manifest == nil || !manifest.WantsBatchComplete {
			continue
		}
		perPlugin := event
		if manifest.WantsMailboxState && provider != nil {
			perPlugin.MailboxState = provider(event.Mailboxes, manifest.Wants)
		} else {
			perPlugin.MailboxState = nil
		}
		payload, err := json.Marshal(perPlugin)
		if err != nil {
			slog.Warn("marshal batch_complete payload", "plugin", manifest.Name, "err", err)
			continue
		}
		if err := c.SendEvent(MethodBatchComplete, payload); err != nil {
			slog.Warn("send batch_complete", "plugin", manifest.Name, "err", err)
		}
	}
}
