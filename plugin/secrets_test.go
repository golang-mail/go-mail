package plugin

import (
	"reflect"
	"sort"
	"testing"
)

func TestIsSecretKey(t *testing.T) {
	cases := map[string]bool{
		"webhook":       true,
		"webhooks":      true,
		"webhook_url":   true,
		"token":         true,
		"bot_token":     true,
		"api_key":       true,
		"api-key":       true,
		"apikey":        true,
		"password":      true,
		"passwd":        true,
		"client_secret": true,
		"credential":    true,
		"Authorization": true,
		"AUTHORIZATION": true,
		"username":      false, // important — must NOT match (no "user" pattern)
		"host":          false,
		"port":          false,
		"icon_url":      false,
		"channel":       false,
		"timeout":       false,
		"ai_timeout":    false,
		"verbose":       false,
	}
	for in, want := range cases {
		if got := IsSecretKey(in); got != want {
			t.Errorf("IsSecretKey(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestMaskSpecSecrets_NestedMap covers the common slack-notify shape:
// webhooks is a map of names → URLs, all values should be masked because
// the parent key "webhooks" matched.
func TestMaskSpecSecrets_NestedMap(t *testing.T) {
	spec := Spec{
		Name: "slack-notify",
		Config: map[string]any{
			"username": "go-mail",
			"webhooks": map[string]any{
				"general": "https://hooks.slack.com/services/T/B/x",
				"alerts":  "https://hooks.slack.com/services/T/B/y",
			},
		},
	}
	got, paths := MaskSpecSecrets(spec)

	gotCfg, _ := got.Config.(map[string]any)
	gotWebhooks, _ := gotCfg["webhooks"].(map[string]any)
	if gotWebhooks["general"] != SecretMask || gotWebhooks["alerts"] != SecretMask {
		t.Errorf("webhook values should be masked: %+v", gotWebhooks)
	}
	if gotCfg["username"] != "go-mail" {
		t.Errorf("username should NOT be masked: %v", gotCfg["username"])
	}

	sort.Strings(paths)
	want := []string{"config.webhooks.alerts", "config.webhooks.general"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("masked paths = %v, want %v", paths, want)
	}
}

// TestMaskSpecSecrets_TopLevelSecretKey covers single-leaf secrets like
// `bot_token: "..."` directly under config — common in telegram-notify.
func TestMaskSpecSecrets_TopLevelSecretKey(t *testing.T) {
	spec := Spec{
		Name: "telegram-notify",
		Config: map[string]any{
			"bot_token": "1234:ABCDEF",
			"chat_id":   "-100123",
		},
	}
	got, paths := MaskSpecSecrets(spec)
	cfg, _ := got.Config.(map[string]any)
	if cfg["bot_token"] != SecretMask {
		t.Errorf("bot_token should be masked, got %v", cfg["bot_token"])
	}
	if cfg["chat_id"] != "-100123" {
		t.Error("chat_id should not be masked (no secret-pattern hit)")
	}
	if len(paths) != 1 || paths[0] != "config.bot_token" {
		t.Errorf("paths = %v, want [config.bot_token]", paths)
	}
}

// TestMaskSpecSecrets_NilConfig confirms nothing crashes when the operator
// hasn't set any plugin config yet.
func TestMaskSpecSecrets_NilConfig(t *testing.T) {
	spec := Spec{Name: "noop"}
	got, paths := MaskSpecSecrets(spec)
	if got.Config != nil {
		t.Errorf("nil config should stay nil; got %v", got.Config)
	}
	if len(paths) != 0 {
		t.Errorf("nil config should yield no paths; got %v", paths)
	}
}

// TestMaskSpecSecrets_DoesNotMutateInput confirms the input spec's Config map
// isn't modified — handlers re-emit the on-disk spec, then merge unmasked
// values back, and that round-trip would corrupt state if the disk view got
// rewritten during masking.
func TestMaskSpecSecrets_DoesNotMutateInput(t *testing.T) {
	orig := map[string]any{
		"webhooks": map[string]any{"general": "https://real"},
	}
	spec := Spec{Name: "x", Config: orig}
	_, _ = MaskSpecSecrets(spec)
	got := orig["webhooks"].(map[string]any)["general"]
	if got != "https://real" {
		t.Errorf("input config mutated: got %v, want https://real", got)
	}
}

func TestUnmaskFromDisk_RestoresMaskedValues(t *testing.T) {
	onDisk := map[string]any{
		"username": "old-name",
		"webhooks": map[string]any{
			"general": "https://real-general",
			"alerts":  "https://real-alerts",
		},
	}
	incoming := map[string]any{
		"username": "new-name", // operator edited
		"webhooks": map[string]any{
			"general": SecretMask,           // unchanged → restore from disk
			"alerts":  "https://new-alerts", // operator replaced
		},
	}
	out := UnmaskFromDisk(incoming, onDisk).(map[string]any)
	if out["username"] != "new-name" {
		t.Errorf("non-secret edit should pass through; got %v", out["username"])
	}
	webhooks := out["webhooks"].(map[string]any)
	if webhooks["general"] != "https://real-general" {
		t.Errorf("masked secret should restore from disk; got %v", webhooks["general"])
	}
	if webhooks["alerts"] != "https://new-alerts" {
		t.Errorf("operator-overwritten secret should pass through; got %v", webhooks["alerts"])
	}
}

// TestUnmaskFromDisk_NewKeyInIncoming confirms operator-added secrets (keys
// not present on disk) pass through unchanged — operators can add a fresh
// webhook key without the merge dropping it.
func TestUnmaskFromDisk_NewKeyInIncoming(t *testing.T) {
	onDisk := map[string]any{"webhooks": map[string]any{"general": "u1"}}
	incoming := map[string]any{
		"webhooks": map[string]any{
			"general": SecretMask,
			"new":     "https://added",
		},
	}
	out := UnmaskFromDisk(incoming, onDisk).(map[string]any)
	webhooks := out["webhooks"].(map[string]any)
	if webhooks["general"] != "u1" {
		t.Errorf("existing masked key should restore; got %v", webhooks["general"])
	}
	if webhooks["new"] != "https://added" {
		t.Errorf("newly-added key should pass through; got %v", webhooks["new"])
	}
}

// TestUnmaskFromDisk_TypeMismatchIncomingWins covers the rare case where
// the operator changed a secret-bearing key from string to a structured
// value (or vice versa). Incoming wins, no descent.
func TestUnmaskFromDisk_TypeMismatchIncomingWins(t *testing.T) {
	onDisk := map[string]any{"webhooks": "https://old-flat-string"}
	incoming := map[string]any{
		"webhooks": map[string]any{"general": "https://new-nested"},
	}
	out := UnmaskFromDisk(incoming, onDisk).(map[string]any)
	webhooks, ok := out["webhooks"].(map[string]any)
	if !ok {
		t.Fatalf("expected new structured shape; got %T", out["webhooks"])
	}
	if webhooks["general"] != "https://new-nested" {
		t.Errorf("new shape should pass through; got %v", webhooks["general"])
	}
}

// TestMaskAllSpecs_PerPluginPaths confirms the per-spec path map carries
// only the plugins that actually had secrets — keeps the wire payload tight.
func TestMaskAllSpecs_PerPluginPaths(t *testing.T) {
	cfg := &Config{Plugins: []Spec{
		{Name: "slack", Config: map[string]any{"webhooks": map[string]any{"g": "u"}}},
		{Name: "noop", Config: map[string]any{"username": "go-mail"}},
	}}
	_, paths := MaskAllSpecs(cfg)
	if _, ok := paths["slack"]; !ok {
		t.Error("slack should appear in per-plugin paths map")
	}
	if _, ok := paths["noop"]; ok {
		t.Error("noop has no secrets, should not appear in paths map")
	}
}
