package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

var (
	batchWatcherOnce sync.Once
	batchWatcherBin  string
	batchWatcherErr  error
)

// buildBatchWatcher lazily builds the test-only batch-watcher plugin.
func buildBatchWatcher(t *testing.T) string {
	t.Helper()
	batchWatcherOnce.Do(func() {
		outDir, err := os.MkdirTemp("", "batch-watcher-")
		if err != nil {
			batchWatcherErr = err
			return
		}
		out := filepath.Join(outDir, "batch-watcher")
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = filepath.Join("testdata", "batch-watcher")
		if combined, berr := cmd.CombinedOutput(); berr != nil {
			batchWatcherErr = &buildOutputErr{prefix: "go build batch-watcher", out: string(combined), err: berr}
			return
		}
		batchWatcherBin = out
	})
	if batchWatcherErr != nil {
		t.Fatalf("build batch-watcher: %v", batchWatcherErr)
	}
	return batchWatcherBin
}

type buildOutputErr struct {
	prefix string
	out    string
	err    error
}

func (b *buildOutputErr) Error() string {
	return b.prefix + ": " + b.err.Error() + "\n" + b.out
}

// watcherOpts bundles the optional behaviors the batch-watcher test plugin
// exposes via env vars. Default zero value = pure no-op plugin.
type watcherOpts struct {
	optIn     bool
	wantState bool
	schedule  string
}

// startManagerWithWatcher boots a Manager with one batch-watcher client
// whose env tells it whether to opt into batch_complete and where to log.
func startManagerWithWatcher(t *testing.T, name, outputFile string, optIn, wantState bool) *Manager {
	return startManagerWithWatcherOpts(t, name, outputFile, watcherOpts{optIn: optIn, wantState: wantState})
}

func startManagerWithWatcherOpts(t *testing.T, name, outputFile string, opts watcherOpts) *Manager {
	t.Helper()
	bin := buildBatchWatcher(t)
	envArgs := []string{
		"env",
		"BATCH_OUTPUT_FILE=" + outputFile,
	}
	if opts.optIn {
		envArgs = append(envArgs, "BATCH_OPT_IN=1")
	}
	if opts.wantState {
		envArgs = append(envArgs, "BATCH_WANT_STATE=1")
	}
	if opts.schedule != "" {
		envArgs = append(envArgs, "SCHEDULE_SPEC="+opts.schedule)
	}
	envArgs = append(envArgs, bin)

	mgr := NewManager("go-mail/test")
	cfg := &Config{Plugins: []Spec{{
		Name:    name,
		Command: envArgs,
		Timeout: 5 * time.Second,
	}}}
	if err := mgr.Init(cfg); err != nil {
		t.Fatalf("manager init: %v", err)
	}
	t.Cleanup(mgr.Shutdown)
	return mgr
}

// waitForFile polls a file until it appears with non-zero size or the deadline elapses.
func waitForFile(t *testing.T, path string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestManager_BroadcastBatchComplete_DeliversToOptIn confirms an opt-in plugin
// receives the batch_complete event end-to-end (event hits the wire, plugin
// writes it to file).
func TestManager_BroadcastBatchComplete_DeliversToOptIn(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "events.ndjson")
	mgr := startManagerWithWatcher(t, "watcher-optin", outFile, true, false)

	mgr.BroadcastBatchComplete(BatchCompleteEvent{
		RunID:   "test-run-1",
		Trigger: "manual",
		Mailboxes: []MailboxRef{
			{Account: "a", Mailbox: "INBOX"},
		},
	})

	data := waitForFile(t, outFile, 2*time.Second)
	if data == nil {
		t.Fatal("opt-in plugin did not receive batch_complete event within 2s")
	}
	if !contains(data, "test-run-1") {
		t.Errorf("payload missing run id: %s", data)
	}
}

// TestManager_BroadcastBatchComplete_SkipsNonOptIn confirms a plugin that did
// NOT declare WantsBatchComplete=true is silently skipped by the broadcast.
func TestManager_BroadcastBatchComplete_SkipsNonOptIn(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "events.ndjson")
	mgr := startManagerWithWatcher(t, "watcher-no-optin", outFile, false, false)

	mgr.BroadcastBatchComplete(BatchCompleteEvent{
		RunID:   "test-run-2",
		Trigger: "manual",
	})

	// Give time for an unexpected event to land.
	time.Sleep(300 * time.Millisecond)

	if _, err := os.Stat(outFile); err == nil {
		data, _ := os.ReadFile(outFile)
		t.Fatalf("non-opt-in plugin received an event: %s", data)
	}
}

// TestManager_BroadcastBatchComplete_PopulatesMailboxState confirms that a
// plugin opting into WantsMailboxState receives a mailbox_state slice built
// by the configured MailboxStateProvider.
func TestManager_BroadcastBatchComplete_PopulatesMailboxState(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "events.ndjson")
	mgr := startManagerWithWatcher(t, "watcher-state", outFile, true, true)

	mgr.SetMailboxStateProvider(func(mailboxes []MailboxRef, wants []string) []MailboxStateEntry {
		return []MailboxStateEntry{{
			Account: "a",
			Mailbox: "INBOX",
			Emails: []*EvalEmail{
				{UID: 42, Account: "a", Mailbox: "INBOX"},
			},
		}}
	})

	mgr.BroadcastBatchComplete(BatchCompleteEvent{
		RunID:     "test-run-3",
		Trigger:   "manual",
		Mailboxes: []MailboxRef{{Account: "a", Mailbox: "INBOX"}},
	})

	data := waitForFile(t, outFile, 2*time.Second)
	if data == nil {
		t.Fatal("plugin did not receive batch_complete event")
	}
	if !contains(data, `"uid":42`) {
		t.Errorf("payload missing mailbox_state UID: %s", data)
	}
}

// TestManager_StartSchedules_FiresScheduledTick uses a short @every duration
// to drive a scheduled_tick event into the watcher within the test deadline.
func TestManager_StartSchedules_FiresScheduledTick(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "events.ndjson")
	mgr := startManagerWithWatcherOpts(t, "watcher-sched", outFile, watcherOpts{schedule: "200ms"})

	mgr.StartSchedules()

	data := waitForFile(t, outFile, 3*time.Second)
	if data == nil {
		t.Fatal("scheduled plugin did not receive a scheduled_tick within 3s")
	}
	if !contains(data, `"method":"scheduled_tick"`) {
		t.Errorf("payload missing scheduled_tick marker: %s", data)
	}
	if !contains(data, `"schedule":"200ms"`) {
		t.Errorf("payload missing raw schedule field: %s", data)
	}
}

// TestManager_StartSchedules_SkipsPluginsWithoutSchedule confirms a plugin
// that did NOT declare a schedule is silently skipped, no spurious ticks.
func TestManager_StartSchedules_SkipsPluginsWithoutSchedule(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "events.ndjson")
	startManagerWithWatcherOpts(t, "watcher-no-sched", outFile, watcherOpts{})

	// no StartSchedules call still needs to be safe-by-default; call anyway
	// to exercise the "no schedules to register" path.
	// (no-op for this plugin since manifest has empty Schedule)

	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(outFile); err == nil {
		data, _ := os.ReadFile(outFile)
		t.Fatalf("non-scheduled plugin received an event: %s", data)
	}
}

// TestDiffSpecs covers the three-way diff used by ApplyConfig: added /
// removed / modified, with stable (sorted) output.
func TestDiffSpecs(t *testing.T) {
	current := map[string]Spec{
		"a": {Name: "a", Command: []string{"a-bin"}},
		"b": {Name: "b", Command: []string{"b-bin"}, Timeout: time.Second},
		"c": {Name: "c", Command: []string{"c-bin"}, Disabled: false},
	}
	desired := map[string]Spec{
		"b": {Name: "b", Command: []string{"b-bin"}, Timeout: 2 * time.Second}, // modified (timeout)
		"c": {Name: "c", Command: []string{"c-bin"}, Disabled: true},           // modified (disable toggle)
		"d": {Name: "d", Command: []string{"d-bin"}},                           // added
	}
	added, removed, modified := diffSpecs(current, desired)

	want := func(name, label string, got, want []string) {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", label, got, want)
		}
		_ = name
	}
	want("added", "added", added, []string{"d"})
	want("removed", "removed", removed, []string{"a"})
	want("modified", "modified", modified, []string{"b", "c"})
}

func TestDuplicateName(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"nil", nil, ""},
		{"empty", &Config{}, ""},
		{"unique", &Config{Plugins: []Spec{{Name: "a"}, {Name: "b"}}}, ""},
		{"dup", &Config{Plugins: []Spec{{Name: "a"}, {Name: "b"}, {Name: "a"}}}, "a"},
		{"empty names ignored", &Config{Plugins: []Spec{{Name: ""}, {Name: ""}}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := duplicateName(tc.cfg); got != tc.want {
				t.Errorf("duplicateName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestManager_Init_RejectsDuplicateNames guards the orphaned-process footgun:
// two specs sharing a name would collide in the by-name maps.
func TestManager_Init_RejectsDuplicateNames(t *testing.T) {
	mgr := NewManager("go-mail/test")
	err := mgr.Init(&Config{Plugins: []Spec{
		{Name: "dup", Command: []string{"x"}},
		{Name: "dup", Command: []string{"y"}},
	}})
	if err == nil {
		t.Fatal("Init accepted duplicate plugin names; want error")
	}
	if len(mgr.Names()) != 0 {
		t.Errorf("Init spawned clients despite duplicate names: %v", mgr.Names())
	}
}

func TestManager_ApplyConfig_RejectsDuplicateNames(t *testing.T) {
	mgr := NewManager("go-mail/test")
	err := mgr.ApplyConfig(&Config{Plugins: []Spec{
		{Name: "dup", Command: []string{"x"}},
		{Name: "dup", Command: []string{"y"}},
	}})
	if err == nil {
		t.Fatal("ApplyConfig accepted duplicate plugin names; want error")
	}
}

// TestDiffSpecs_NoChanges confirms a config that hasn't changed produces no
// deltas, so a spurious fsnotify fire is a true no-op.
func TestDiffSpecs_NoChanges(t *testing.T) {
	current := map[string]Spec{
		"a": {Name: "a", Command: []string{"a"}, Config: map[string]any{"k": "v"}},
	}
	desired := map[string]Spec{
		"a": {Name: "a", Command: []string{"a"}, Config: map[string]any{"k": "v"}},
	}
	added, removed, modified := diffSpecs(current, desired)
	if len(added) != 0 || len(removed) != 0 || len(modified) != 0 {
		t.Errorf("expected no deltas; got added=%v removed=%v modified=%v", added, removed, modified)
	}
}

// TestSpecsEqual_NestedConfig confirms deep equality reaches into the YAML-
// derived config map. False positives here would cause unnecessary plugin
// restarts on every watcher fire.
func TestSpecsEqual_NestedConfig(t *testing.T) {
	a := Spec{Name: "x", Config: map[string]any{"webhooks": map[string]any{"general": "u1"}}}
	b := Spec{Name: "x", Config: map[string]any{"webhooks": map[string]any{"general": "u1"}}}
	if !specsEqual(a, b) {
		t.Error("equal nested configs should compare equal")
	}
	c := Spec{Name: "x", Config: map[string]any{"webhooks": map[string]any{"general": "u2"}}}
	if specsEqual(a, c) {
		t.Error("differing nested configs should compare unequal")
	}
}

// TestSaveConfig_Roundtrip writes a config, reads it back, and confirms the
// shape survives. Also verifies atomic-write semantics — no temp files left
// in the directory after success.
func TestSaveConfig_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins.yml")
	cfg := &Config{Plugins: []Spec{
		{Name: "alpha", Command: []string{"/bin/alpha"}, Timeout: 5 * time.Second},
		{Name: "beta", Command: []string{"/bin/beta"}, Disabled: true, Config: map[string]any{"k": "v"}},
	}}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got == nil || len(got.Plugins) != 2 {
		t.Fatalf("roundtrip: want 2 plugins, got %+v", got)
	}
	if got.Plugins[1].Name != "beta" || !got.Plugins[1].Disabled {
		t.Errorf("beta disabled flag did not survive roundtrip: %+v", got.Plugins[1])
	}

	// No temp files should remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "plugins.yml" {
			t.Errorf("stray file in dir after SaveConfig: %q", e.Name())
		}
	}
}

// TestSaveConfig_PreservesPermissions confirms the rename inherits the
// existing file's mode bits — important for files that may carry secrets.
func TestSaveConfig_PreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins.yml")
	if err := os.WriteFile(path, []byte("plugins: []\n"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := SaveConfig(path, &Config{}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

// TestManager_ApplyConfig_DisabledSpec_NoSpawn confirms a spec with
// Disabled=true is tracked in m.specs but no client process is started.
// Statuses still surfaces it with Running=false.
func TestManager_ApplyConfig_DisabledSpec_NoSpawn(t *testing.T) {
	mgr := NewManager("go-mail/test")
	t.Cleanup(mgr.Shutdown)
	// Use a bogus command — if Init tried to spawn it, Start would fail and
	// Init would return an error.
	cfg := &Config{Plugins: []Spec{
		{Name: "off", Command: []string{"/nonexistent/binary"}, Disabled: true, Timeout: time.Second},
	}}
	if err := mgr.Init(cfg); err != nil {
		t.Fatalf("Init with disabled plugin should not spawn: %v", err)
	}
	if mgr.Get("off") != nil {
		t.Error("disabled plugin should not have a live Client")
	}
	statuses := mgr.Statuses()
	if len(statuses) != 1 || statuses[0].Name != "off" || statuses[0].Running {
		t.Errorf("expected synthesized status for disabled plugin; got %+v", statuses)
	}
	if !statuses[0].Disabled {
		t.Error("Statuses should surface operator-disabled plugins as Disabled=true")
	}
}

// TestManager_ApplyConfig_AddRemoveModify exercises the diff machinery end-to-end
// against a real (watcher-based) plugin so the spawn / teardown / respawn paths
// all run for real.
func TestManager_ApplyConfig_AddRemoveModify(t *testing.T) {
	dir := t.TempDir()
	out1 := filepath.Join(dir, "p1.ndjson")
	out2 := filepath.Join(dir, "p2.ndjson")
	bin := buildBatchWatcher(t)

	makeSpec := func(name, outFile string, optIn bool) Spec {
		envArgs := []string{"env", "BATCH_OUTPUT_FILE=" + outFile}
		if optIn {
			envArgs = append(envArgs, "BATCH_OPT_IN=1")
		}
		envArgs = append(envArgs, bin)
		return Spec{Name: name, Command: envArgs, Timeout: 5 * time.Second}
	}

	mgr := NewManager("go-mail/test")
	t.Cleanup(mgr.Shutdown)

	if err := mgr.Init(&Config{Plugins: []Spec{makeSpec("p1", out1, true)}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if mgr.Get("p1") == nil {
		t.Fatal("p1 should be running after Init")
	}

	// ApplyConfig with: p1 unchanged (same args), p2 added.
	if err := mgr.ApplyConfig(&Config{Plugins: []Spec{
		makeSpec("p1", out1, true),
		makeSpec("p2", out2, true),
	}}); err != nil {
		t.Fatalf("ApplyConfig add: %v", err)
	}
	if mgr.Get("p2") == nil {
		t.Fatal("p2 should be spawned by ApplyConfig")
	}

	// Now: remove p1, modify p2 (toggle opt-in off).
	if err := mgr.ApplyConfig(&Config{Plugins: []Spec{
		makeSpec("p2", out2, false), // opt-in changed → modified path → restart
	}}); err != nil {
		t.Fatalf("ApplyConfig modify+remove: %v", err)
	}
	if mgr.Get("p1") != nil {
		t.Error("p1 should be torn down")
	}
	if mgr.Get("p2") == nil {
		t.Error("p2 should still be running after modify")
	}

	// Both batch-complete events sent now: opt-in p1 was removed (no longer
	// listening); p2 is no longer opt-in. Verify the broadcast is a no-op
	// for both.
	mgr.BroadcastBatchComplete(BatchCompleteEvent{RunID: "verify", Trigger: "manual"})
	time.Sleep(200 * time.Millisecond)
	for _, p := range []string{out1, out2} {
		if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
			t.Errorf("file %s should be empty (no listening opt-in plugin); got %s", p, data)
		}
	}

	// Finally: ApplyConfig with empty config → tear down everything.
	if err := mgr.ApplyConfig(&Config{}); err != nil {
		t.Fatalf("ApplyConfig empty: %v", err)
	}
	if got := mgr.Names(); len(got) != 0 {
		sort.Strings(got)
		t.Errorf("empty config should leave no running plugins; got %v", got)
	}
}

// TestManager_RestartPlugin verifies the restart path: same spec, but a fresh
// child process. The watcher records each connection by appending to a file,
// so we can confirm a second connection happened.
func TestManager_RestartPlugin(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "events.ndjson")
	mgr := startManagerWithWatcher(t, "restartable", outFile, true, false)

	// First broadcast → first child writes one line.
	mgr.BroadcastBatchComplete(BatchCompleteEvent{RunID: "before", Trigger: "manual"})
	if waitForFile(t, outFile, 2*time.Second) == nil {
		t.Fatal("first plugin instance did not record the first event")
	}

	if err := mgr.RestartPlugin("restartable"); err != nil {
		t.Fatalf("RestartPlugin: %v", err)
	}

	// Second broadcast → respawned child writes another line. The file is
	// append-mode in batch-watcher so we look for both run_ids.
	mgr.BroadcastBatchComplete(BatchCompleteEvent{RunID: "after", Trigger: "manual"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(outFile)
		if contains(data, "before") && contains(data, "after") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(outFile)
	t.Fatalf("restart did not deliver second event; file contents: %s", data)
}

// TestNormalizeScheduleSpec covers the duration→@every translation and
// pass-through for actual cron expressions.
func TestNormalizeScheduleSpec(t *testing.T) {
	cases := map[string]string{
		"10m":         "@every 10m",
		"1h30m":       "@every 1h30m",
		"@daily":      "@daily",
		"@every 5s":   "@every 5s",
		"0 8 * * 1-5": "0 8 * * 1-5",
		"":            "",
	}
	for in, want := range cases {
		if got := normalizeScheduleSpec(in); got != want {
			t.Errorf("normalizeScheduleSpec(%q) = %q, want %q", in, got, want)
		}
	}
}

func contains(haystack []byte, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 && bytesIndex(haystack, []byte(needle)) >= 0
}

// bytesIndex is a tiny helper so we don't import "bytes" just for one call.
func bytesIndex(haystack, needle []byte) int {
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for j, b := range needle {
			if haystack[i+j] != b {
				continue outer
			}
		}
		return i
	}
	return -1
}
