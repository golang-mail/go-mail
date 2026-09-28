package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/plugin"
)

// fakeMgr is a stand-in PluginManagerAPI that records mutations the handlers
// drive through it. Specs live in memory; ApplyConfig replaces the whole set
// (matching the real Manager's diff-and-apply semantics from the outside).
type fakeMgr struct {
	mu        sync.Mutex
	specs     map[string]plugin.Spec
	statuses  map[string]plugin.Status
	manifests map[string]*plugin.Manifest

	applyCalls   []*plugin.Config
	restartCalls []string
	triggerCalls []string
	triggerErr   error
}

func newFakeMgr() *fakeMgr {
	return &fakeMgr{
		specs:     map[string]plugin.Spec{},
		statuses:  map[string]plugin.Status{},
		manifests: map[string]*plugin.Manifest{},
	}
}

func (f *fakeMgr) Statuses() []plugin.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]plugin.Status, 0, len(f.statuses))
	for _, s := range f.statuses {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (f *fakeMgr) GetSpec(name string) (plugin.Spec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.specs[name]
	return s, ok
}

func (f *fakeMgr) Specs() *plugin.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := &plugin.Config{}
	names := make([]string, 0, len(f.specs))
	for n := range f.specs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		cfg.Plugins = append(cfg.Plugins, f.specs[n])
	}
	return cfg
}

func (f *fakeMgr) ManifestOf(name string) *plugin.Manifest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.manifests[name]
}

func (f *fakeMgr) ApplyConfig(cfg *plugin.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyCalls = append(f.applyCalls, cfg)
	f.specs = map[string]plugin.Spec{}
	if cfg != nil {
		for _, s := range cfg.Plugins {
			f.specs[s.Name] = s
		}
	}
	return nil
}

func (f *fakeMgr) RestartPlugin(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartCalls = append(f.restartCalls, name)
	return nil
}

func (f *fakeMgr) TriggerSchedule(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triggerCalls = append(f.triggerCalls, name)
	return f.triggerErr
}

// setupHandlerTest points cache.ConfigDir at a temp dir, seeds plugins.yml,
// and returns the directory + fake manager. t.Cleanup restores ConfigDir.
func setupHandlerTest(t *testing.T, initial *plugin.Config) (*fakeMgr, string) {
	t.Helper()
	prev := cache.ConfigDir
	dir := t.TempDir()
	cache.ConfigDir = dir
	t.Cleanup(func() { cache.ConfigDir = prev })

	if initial != nil {
		path := filepath.Join(dir, "plugins.yml")
		if err := plugin.SaveConfig(path, initial); err != nil {
			t.Fatalf("seed plugins.yml: %v", err)
		}
	}

	mgr := newFakeMgr()
	if initial != nil {
		for _, s := range initial.Plugins {
			mgr.specs[s.Name] = s
		}
	}
	return mgr, dir
}

func TestPluginsListGet_MasksSecrets(t *testing.T) {
	mgr, _ := setupHandlerTest(t, &plugin.Config{Plugins: []plugin.Spec{
		{Name: "slack", Command: []string{"/bin/slack"}, Config: map[string]any{
			"webhooks": map[string]any{"general": "https://secret"},
			"username": "go-mail",
		}},
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/plugins", nil)
	rr := httptest.NewRecorder()
	pluginsHandler(mgr)(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out []pluginDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body: %s", err, rr.Body.String())
	}
	if len(out) != 1 {
		t.Fatalf("want 1 plugin, got %d", len(out))
	}
	cfg, _ := out[0].Spec.Config.(map[string]any)
	webhooks, _ := cfg["webhooks"].(map[string]any)
	if webhooks["general"] != plugin.SecretMask {
		t.Errorf("webhook should be masked; got %v", webhooks["general"])
	}
	if cfg["username"] != "go-mail" {
		t.Errorf("non-secret should pass through; got %v", cfg["username"])
	}
	if len(out[0].ConfigSecretPaths) != 1 || out[0].ConfigSecretPaths[0] != "config.webhooks.general" {
		t.Errorf("config_secret_paths = %v, want [config.webhooks.general]", out[0].ConfigSecretPaths)
	}
}

func TestPluginsListGet_NilManagerReturnsEmpty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/plugins", nil)
	rr := httptest.NewRecorder()
	pluginsHandler(nil)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Body.String(); got != "[]\n" {
		t.Errorf("body = %q, want []", got)
	}
}

func TestPluginItemGet_FoundAndMissing(t *testing.T) {
	mgr, _ := setupHandlerTest(t, &plugin.Config{Plugins: []plugin.Spec{
		{Name: "slack", Command: []string{"/bin/slack"}},
	}})

	// Existing
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/slack", nil)
	rr := httptest.NewRecorder()
	pluginItemHandler(mgr)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var detail pluginDetail
	_ = json.Unmarshal(rr.Body.Bytes(), &detail)
	if detail.Name != "slack" {
		t.Errorf("got name %q, want slack", detail.Name)
	}

	// Missing
	req2 := httptest.NewRequest(http.MethodGet, "/api/plugins/nope", nil)
	rr2 := httptest.NewRecorder()
	pluginItemHandler(mgr)(rr2, req2)
	if rr2.Code != http.StatusNotFound {
		t.Errorf("missing plugin should 404; got %d", rr2.Code)
	}
}

func TestPluginsPost_AddsAndPersists(t *testing.T) {
	mgr, dir := setupHandlerTest(t, nil)

	body := plugin.Spec{Name: "new-plugin", Command: []string{"/bin/np"}}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/plugins", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	pluginsHandler(mgr)(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	// File on disk should have the entry.
	cfg, err := plugin.LoadConfig(filepath.Join(dir, "plugins.yml"))
	if err != nil || cfg == nil {
		t.Fatalf("load after post: %v", err)
	}
	if len(cfg.Plugins) != 1 || cfg.Plugins[0].Name != "new-plugin" {
		t.Errorf("plugins.yml not updated: %+v", cfg)
	}
	// ApplyConfig should have been called exactly once.
	if len(mgr.applyCalls) != 1 {
		t.Errorf("ApplyConfig calls = %d, want 1", len(mgr.applyCalls))
	}
}

func TestPluginsPost_DuplicateNameConflicts(t *testing.T) {
	mgr, _ := setupHandlerTest(t, &plugin.Config{Plugins: []plugin.Spec{
		{Name: "slack", Command: []string{"/bin/slack"}},
	}})
	body := plugin.Spec{Name: "slack", Command: []string{"/bin/dup"}}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/plugins", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	pluginsHandler(mgr)(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("duplicate post should 409; got %d", rr.Code)
	}
}

func TestPluginItemPut_UnmaskRestoresSecret(t *testing.T) {
	mgr, dir := setupHandlerTest(t, &plugin.Config{Plugins: []plugin.Spec{
		{Name: "slack", Command: []string{"/bin/slack"}, Config: map[string]any{
			"webhooks": map[string]any{"general": "https://real-url"},
			"username": "old",
		}},
	}})

	// Operator sends back: masked webhook + new username.
	incoming := plugin.Spec{
		Name: "slack", Command: []string{"/bin/slack"},
		Config: map[string]any{
			"webhooks": map[string]any{"general": plugin.SecretMask},
			"username": "new",
		},
	}
	raw, _ := json.Marshal(incoming)
	req := httptest.NewRequest(http.MethodPut, "/api/plugins/slack", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	pluginItemHandler(mgr)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// On disk: webhook URL should be restored, username updated.
	cfg, _ := plugin.LoadConfig(filepath.Join(dir, "plugins.yml"))
	if cfg == nil || len(cfg.Plugins) != 1 {
		t.Fatalf("disk state: %+v", cfg)
	}
	got, _ := cfg.Plugins[0].Config.(map[string]any)
	webhooks, _ := got["webhooks"].(map[string]any)
	if webhooks["general"] != "https://real-url" {
		t.Errorf("webhook should restore from disk; got %v", webhooks["general"])
	}
	if got["username"] != "new" {
		t.Errorf("username edit lost; got %v", got["username"])
	}
}

func TestPluginItemDelete_RemovesAndPersists(t *testing.T) {
	mgr, dir := setupHandlerTest(t, &plugin.Config{Plugins: []plugin.Spec{
		{Name: "a", Command: []string{"/bin/a"}},
		{Name: "b", Command: []string{"/bin/b"}},
	}})

	req := httptest.NewRequest(http.MethodDelete, "/api/plugins/a", nil)
	rr := httptest.NewRecorder()
	pluginItemHandler(mgr)(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	cfg, _ := plugin.LoadConfig(filepath.Join(dir, "plugins.yml"))
	if cfg == nil || len(cfg.Plugins) != 1 || cfg.Plugins[0].Name != "b" {
		t.Errorf("expected only 'b' left on disk; got %+v", cfg)
	}
}

func TestPluginItemDelete_MissingReturns404(t *testing.T) {
	mgr, _ := setupHandlerTest(t, &plugin.Config{})
	req := httptest.NewRequest(http.MethodDelete, "/api/plugins/ghost", nil)
	rr := httptest.NewRecorder()
	pluginItemHandler(mgr)(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("missing delete should 404; got %d", rr.Code)
	}
}

func TestPluginRestart_CallsManager(t *testing.T) {
	mgr, _ := setupHandlerTest(t, &plugin.Config{Plugins: []plugin.Spec{
		{Name: "slack", Command: []string{"/bin/slack"}},
	}})
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/slack/restart", nil)
	rr := httptest.NewRecorder()
	pluginItemHandler(mgr)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if len(mgr.restartCalls) != 1 || mgr.restartCalls[0] != "slack" {
		t.Errorf("RestartPlugin calls = %v, want [slack]", mgr.restartCalls)
	}
}

func TestPluginEnableDisable_FlipsDisabledFlag(t *testing.T) {
	mgr, dir := setupHandlerTest(t, &plugin.Config{Plugins: []plugin.Spec{
		{Name: "slack", Command: []string{"/bin/slack"}, Disabled: false},
	}})

	// Disable.
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/slack/disable", nil)
	rr := httptest.NewRecorder()
	pluginItemHandler(mgr)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	cfg, _ := plugin.LoadConfig(filepath.Join(dir, "plugins.yml"))
	if cfg == nil || !cfg.Plugins[0].Disabled {
		t.Errorf("expected Disabled=true on disk after disable; got %+v", cfg.Plugins[0])
	}

	// Enable.
	req2 := httptest.NewRequest(http.MethodPost, "/api/plugins/slack/enable", nil)
	rr2 := httptest.NewRecorder()
	pluginItemHandler(mgr)(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("enable status = %d, want 200; body: %s", rr2.Code, rr2.Body.String())
	}
	cfg2, _ := plugin.LoadConfig(filepath.Join(dir, "plugins.yml"))
	if cfg2 == nil || cfg2.Plugins[0].Disabled {
		t.Errorf("expected Disabled=false on disk after enable; got %+v", cfg2.Plugins[0])
	}
}

func TestPluginsHandler_RejectsUnknownMethod(t *testing.T) {
	mgr, _ := setupHandlerTest(t, &plugin.Config{})
	req := httptest.NewRequest(http.MethodPatch, "/api/plugins", nil)
	rr := httptest.NewRecorder()
	pluginsHandler(mgr)(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("PATCH should 405; got %d", rr.Code)
	}
}

func TestPluginItemHandler_UnknownAction(t *testing.T) {
	mgr, _ := setupHandlerTest(t, &plugin.Config{Plugins: []plugin.Spec{
		{Name: "slack", Command: []string{"/bin/slack"}},
	}})
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/slack/bogus", nil)
	rr := httptest.NewRecorder()
	pluginItemHandler(mgr)(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown action should 404; got %d", rr.Code)
	}
}

// TestPluginUsageFromFilters exercises the filter scan that produces the
// "used by N filters" metadata. Independent of the manager — runs over
// filters.yml on disk.
func TestPluginUsageFromFilters(t *testing.T) {
	prev := cache.ConfigDir
	dir := t.TempDir()
	cache.ConfigDir = dir
	t.Cleanup(func() { cache.ConfigDir = prev })

	// Seed filters.yml with a couple plugin: references.
	filtersYAML := `filters:
  - name: f1
    mailboxes: ["INBOX"]
    conditions:
      - field: plugin:slack
        operator: matches
        value: general
    actions: []
  - name: f2
    mailboxes: ["INBOX"]
    conditions:
      - field: plugin:slack
        operator: matches
        value: alerts
    actions: []
  - name: f3
    mailboxes: ["INBOX"]
    conditions:
      - field: subject
        operator: contains
        value: hi
    actions: []
`
	if err := os.WriteFile(filepath.Join(dir, "filters.yml"), []byte(filtersYAML), 0o644); err != nil {
		t.Fatalf("seed filters.yml: %v", err)
	}

	usage := pluginUsageFromFilters()
	got, ok := usage["slack"]
	if !ok {
		t.Fatal("expected slack to appear in usage")
	}
	if got.FilterCount != 2 {
		t.Errorf("slack FilterCount = %d, want 2", got.FilterCount)
	}
	sort.Strings(got.Filters)
	if len(got.Filters) != 2 || got.Filters[0] != "f1" || got.Filters[1] != "f2" {
		t.Errorf("slack Filters = %v, want [f1 f2]", got.Filters)
	}
}
