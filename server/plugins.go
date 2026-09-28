package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/plugin"
)

// pluginDetail is the wire shape for both the list and item GET endpoints.
// Spec is masked (secrets replaced by plugin.SecretMask). config_secret_paths
// lists which dotted paths inside Spec.Config are masked, so the UI can render
// those fields with a "secret — leave masked to keep unchanged" affordance.
type pluginDetail struct {
	Name              string           `json:"name"`
	Spec              plugin.Spec      `json:"spec"`
	ConfigSecretPaths []string         `json:"config_secret_paths,omitempty"`
	Manifest          *plugin.Manifest `json:"manifest,omitempty"`
	Status            plugin.Status    `json:"status"`
	Usage             pluginUsage      `json:"usage"`
}

// pluginUsage surfaces which filters reference this plugin. Helps operators
// avoid the "deleted a plugin and now filters silently no-op" footgun.
type pluginUsage struct {
	FilterCount int      `json:"filter_count"`
	Filters     []string `json:"filters,omitempty"`
}

// pluginsHandler implements GET /api/plugins (list) and POST /api/plugins (add).
func pluginsHandler(mgr PluginManagerAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			pluginsListGet(w, r, mgr)
		case http.MethodPost:
			pluginsPost(w, r, mgr)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// pluginItemHandler implements /api/plugins/{name}[/action]:
//
//	GET    /api/plugins/{name}            — one plugin detail
//	PUT    /api/plugins/{name}            — replace spec/config
//	DELETE /api/plugins/{name}            — remove plugin entry
//	POST   /api/plugins/{name}/restart    — restart child process
//	POST   /api/plugins/{name}/enable     — set disabled=false
//	POST   /api/plugins/{name}/disable    — set disabled=true
func pluginItemHandler(mgr PluginManagerAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/plugins/")
		if rest == "" {
			http.Error(w, "plugin name required", http.StatusBadRequest)
			return
		}
		parts := strings.SplitN(rest, "/", 2)
		name := parts[0]
		action := ""
		if len(parts) == 2 {
			action = parts[1]
		}

		if action == "" {
			switch r.Method {
			case http.MethodGet:
				pluginItemGet(w, r, mgr, name)
			case http.MethodPut:
				pluginItemPut(w, r, mgr, name)
			case http.MethodDelete:
				pluginItemDelete(w, r, mgr, name)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		switch action {
		case "restart":
			pluginRestart(w, r, mgr, name)
		case "enable":
			pluginToggleDisabled(w, r, mgr, name, false)
		case "disable":
			pluginToggleDisabled(w, r, mgr, name, true)
		case "trigger":
			pluginTrigger(w, r, mgr, name)
		default:
			http.Error(w, "unknown action: "+action, http.StatusNotFound)
		}
	}
}

// pluginsListGet returns every configured plugin with its sanitized spec,
// manifest, status, and filter-usage breakdown. Always 200 (empty list when
// no manager is wired or no plugins are configured).
func pluginsListGet(w http.ResponseWriter, r *http.Request, mgr PluginManagerAPI) {
	w.Header().Set("Content-Type", "application/json")
	if mgr == nil {
		_ = json.NewEncoder(w).Encode([]pluginDetail{})
		return
	}

	statusByName := indexStatuses(mgr.Statuses())
	usage := pluginUsageFromFilters()

	cfg := mgr.Specs()
	out := make([]pluginDetail, 0, len(cfg.Plugins))
	for _, spec := range cfg.Plugins {
		masked, paths := plugin.MaskSpecSecrets(spec)
		out = append(out, pluginDetail{
			Name:              spec.Name,
			Spec:              masked,
			ConfigSecretPaths: paths,
			Manifest:          mgr.ManifestOf(spec.Name),
			Status:            statusByName[spec.Name],
			Usage:             usage[spec.Name],
		})
	}
	_ = json.NewEncoder(w).Encode(out)
}

// pluginItemGet emits one plugin's full detail. Returns 404 when the plugin
// isn't configured.
func pluginItemGet(w http.ResponseWriter, r *http.Request, mgr PluginManagerAPI, name string) {
	if mgr == nil {
		http.Error(w, "plugin manager not configured", http.StatusServiceUnavailable)
		return
	}
	spec, ok := mgr.GetSpec(name)
	if !ok {
		http.Error(w, "plugin not found", http.StatusNotFound)
		return
	}
	masked, paths := plugin.MaskSpecSecrets(spec)
	usage := pluginUsageFromFilters()
	statusByName := indexStatuses(mgr.Statuses())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pluginDetail{
		Name:              name,
		Spec:              masked,
		ConfigSecretPaths: paths,
		Manifest:          mgr.ManifestOf(name),
		Status:            statusByName[name],
		Usage:             usage[name],
	})
}

// pluginsPost adds a new plugin spec. Name must be unique. Body is a partial
// plugin.Spec with at least name + command set.
func pluginsPost(w http.ResponseWriter, r *http.Request, mgr PluginManagerAPI) {
	if mgr == nil {
		http.Error(w, "plugin manager not configured", http.StatusServiceUnavailable)
		return
	}
	var spec plugin.Spec
	if err := decodeJSONBody(w, r, &spec); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if spec.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if len(spec.Command) == 0 && spec.Address == "" {
		http.Error(w, "command or address is required", http.StatusBadRequest)
		return
	}
	if _, exists := mgr.GetSpec(spec.Name); exists {
		http.Error(w, "plugin already exists: "+spec.Name, http.StatusConflict)
		return
	}

	err := applyPluginMutation(mgr, func(cfg *plugin.Config) error {
		cfg.Plugins = append(cfg.Plugins, spec)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("plugin added via api", "user", sessionUsername(r), "name", spec.Name)
	pluginItemGet(w, r, mgr, spec.Name)
}

// pluginItemPut replaces the named plugin's spec. Secret values marked with
// plugin.SecretMask in the incoming payload are restored from the on-disk
// values so operators don't need to retype unchanged webhooks/tokens.
func pluginItemPut(w http.ResponseWriter, r *http.Request, mgr PluginManagerAPI, name string) {
	if mgr == nil {
		http.Error(w, "plugin manager not configured", http.StatusServiceUnavailable)
		return
	}
	var incoming plugin.Spec
	if err := decodeJSONBody(w, r, &incoming); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if incoming.Name == "" {
		incoming.Name = name
	}
	if incoming.Name != name {
		http.Error(w, "name in body does not match URL", http.StatusBadRequest)
		return
	}

	err := applyPluginMutation(mgr, func(cfg *plugin.Config) error {
		for i, s := range cfg.Plugins {
			if s.Name == name {
				incoming.Config = plugin.UnmaskFromDisk(incoming.Config, s.Config)
				cfg.Plugins[i] = incoming
				return nil
			}
		}
		return fmt.Errorf("plugin not found: %s", name)
	})
	if err != nil {
		if errors.Is(err, errPluginNotFound) || strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("plugin updated via api", "user", sessionUsername(r), "name", name)
	pluginItemGet(w, r, mgr, name)
}

// pluginItemDelete removes the named plugin from plugins.yml and tears down
// its child process.
func pluginItemDelete(w http.ResponseWriter, r *http.Request, mgr PluginManagerAPI, name string) {
	if mgr == nil {
		http.Error(w, "plugin manager not configured", http.StatusServiceUnavailable)
		return
	}
	err := applyPluginMutation(mgr, func(cfg *plugin.Config) error {
		for i, s := range cfg.Plugins {
			if s.Name == name {
				cfg.Plugins = append(cfg.Plugins[:i], cfg.Plugins[i+1:]...)
				return nil
			}
		}
		return errPluginNotFound
	})
	if errors.Is(err, errPluginNotFound) {
		http.Error(w, "plugin not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("plugin deleted via api", "user", sessionUsername(r), "name", name)
	w.WriteHeader(http.StatusNoContent)
}

// pluginRestart tears down + respawns the child without touching plugins.yml.
// Useful when the operator changed a config option that the plugin only reads
// at init (most do) and wants the child to pick it up immediately, or when a
// crashed plugin needs a manual nudge past its restart backoff.
func pluginRestart(w http.ResponseWriter, r *http.Request, mgr PluginManagerAPI, name string) {
	if mgr == nil {
		http.Error(w, "plugin manager not configured", http.StatusServiceUnavailable)
		return
	}
	if err := mgr.RestartPlugin(name); err != nil {
		if strings.Contains(err.Error(), "not configured") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("plugin restarted via api", "user", sessionUsername(r), "name", name)
	pluginItemGet(w, r, mgr, name)
}

// pluginTrigger fires an immediate scheduled_tick at a scheduled plugin so the
// operator can validate or force-fire it from the UI without waiting for the
// next cron tick. Returns 400 when the plugin doesn't declare wants_schedule,
// 404 when unknown.
func pluginTrigger(w http.ResponseWriter, r *http.Request, mgr PluginManagerAPI, name string) {
	if mgr == nil {
		http.Error(w, "plugin manager not configured", http.StatusServiceUnavailable)
		return
	}
	if err := mgr.TriggerSchedule(name); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "not running"), strings.Contains(msg, "not configured"):
			http.Error(w, msg, http.StatusNotFound)
		case strings.Contains(msg, "does not declare wants_schedule"):
			http.Error(w, msg, http.StatusBadRequest)
		default:
			http.Error(w, msg, http.StatusInternalServerError)
		}
		return
	}
	slog.Info("plugin triggered via api", "user", sessionUsername(r), "name", name)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "triggered"})
}

// pluginToggleDisabled flips the Disabled flag and applies. Useful as a
// "quiet a noisy plugin without uninstalling" affordance.
func pluginToggleDisabled(w http.ResponseWriter, r *http.Request, mgr PluginManagerAPI, name string, disabled bool) {
	if mgr == nil {
		http.Error(w, "plugin manager not configured", http.StatusServiceUnavailable)
		return
	}
	err := applyPluginMutation(mgr, func(cfg *plugin.Config) error {
		for i, s := range cfg.Plugins {
			if s.Name == name {
				cfg.Plugins[i].Disabled = disabled
				return nil
			}
		}
		return errPluginNotFound
	})
	if errors.Is(err, errPluginNotFound) {
		http.Error(w, "plugin not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	action := "enabled"
	if disabled {
		action = "disabled"
	}
	slog.Info("plugin "+action+" via api", "user", sessionUsername(r), "name", name)
	pluginItemGet(w, r, mgr, name)
}

// applyPluginMutation centralizes the load-mutate-save-apply cycle every
// mutating handler runs. Reads plugins.yml from disk, hands the parsed cfg
// to mutate, writes back atomically, then asks the manager to apply the
// diff so the live state matches disk before the HTTP response returns.
func applyPluginMutation(mgr PluginManagerAPI, mutate func(*plugin.Config) error) error {
	path := cache.ConfigPath("plugins.yml")
	cfg, err := plugin.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("load plugins.yml: %w", err)
	}
	if cfg == nil {
		cfg = &plugin.Config{}
	}
	if err := mutate(cfg); err != nil {
		return err
	}
	if err := plugin.SaveConfig(path, cfg); err != nil {
		return fmt.Errorf("save plugins.yml: %w", err)
	}
	if err := mgr.ApplyConfig(cfg); err != nil {
		return fmt.Errorf("apply config: %w", err)
	}
	return nil
}

// errPluginNotFound is the sentinel applyPluginMutation callbacks return when
// the named plugin doesn't exist in plugins.yml.
var errPluginNotFound = errors.New("plugin not found")

// indexStatuses converts a []Status to a name-keyed map for fast lookup
// during list / item rendering.
func indexStatuses(statuses []plugin.Status) map[string]plugin.Status {
	out := make(map[string]plugin.Status, len(statuses))
	for _, s := range statuses {
		out[s.Name] = s
	}
	return out
}

// pluginUsageFromFilters scans filters.yml and counts how many filter rules
// reference each plugin name via a `plugin:<name>` condition field. Cheap
// enough to compute per-request — filters.yml is small.
func pluginUsageFromFilters() map[string]pluginUsage {
	cfg, err := readFiltersFile()
	if err != nil {
		return map[string]pluginUsage{}
	}
	out := map[string]pluginUsage{}
	for _, f := range cfg.Filters {
		for _, c := range f.Conditions {
			field := strings.ToLower(strings.TrimSpace(c.Field))
			if !strings.HasPrefix(field, "plugin:") {
				continue
			}
			name := strings.TrimPrefix(field, "plugin:")
			u := out[name]
			u.FilterCount++
			u.Filters = append(u.Filters, f.Name)
			out[name] = u
		}
	}
	for name, u := range out {
		sort.Strings(u.Filters)
		out[name] = u
	}
	return out
}

// sessionUsername returns a short label for the actor behind a request, for
// audit slog lines. go-mail today is single-admin (one username/password in
// server.yml), so we just label every mutation "operator" — there is no
// per-user attribution to surface. Kept as a function so a future multi-user
// model can replace this with a real username lookup.
func sessionUsername(_ *http.Request) string {
	return "operator"
}
