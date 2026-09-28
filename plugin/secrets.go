package plugin

import (
	"fmt"
	"regexp"
)

// SecretMask is the sentinel value the server places at masked paths when
// emitting a plugin's config over the wire. The PUT/POST path interprets this
// exact string as "no change — use the on-disk value" so operators can edit a
// plugin's non-secret fields without retyping every webhook URL or token.
//
// The string is intentionally distinctive (multi-byte bullets bracket the
// "MASKED" tag) so accidental collisions with a real secret value are
// vanishingly unlikely.
const SecretMask = "•••MASKED•••"

// secretKeyPattern matches any config-key segment whose name suggests a
// secret value. Substring + case-insensitive, so "webhook_url" /
// "WebhookURL" / "webhooks" all match. Patterns chosen to cover the secrets
// the existing plugins carry today (slack-notify webhook URLs, telegram bot
// tokens, gitlab tokens, etc.) without being so broad they swallow benign
// keys like "host" or "user".
var secretKeyPattern = regexp.MustCompile(`(?i)(webhook|token|password|passwd|secret|api[_-]?key|credential|authorization)`)

// IsSecretKey reports whether a config-key name looks like it holds a secret.
// Exported for tests and for callers that want to label fields in the UI.
func IsSecretKey(name string) bool { return secretKeyPattern.MatchString(name) }

// MaskSpecSecrets returns a clone of spec with secret-looking values in its
// Config replaced by SecretMask, plus the dotted paths that were masked.
// Path format is rooted at "config." so the UI can render the same path the
// operator's YAML editor uses.
func MaskSpecSecrets(spec Spec) (Spec, []string) {
	var paths []string
	if spec.Config != nil {
		spec.Config = maskValue(spec.Config, "config", false, &paths)
	}
	return spec, paths
}

// MaskAllSpecs walks a Config and masks each spec's secrets. Returns the
// masked clone plus a per-plugin-name map of masked paths.
func MaskAllSpecs(cfg *Config) (*Config, map[string][]string) {
	if cfg == nil {
		return nil, nil
	}
	out := &Config{Plugins: make([]Spec, 0, len(cfg.Plugins))}
	secrets := map[string][]string{}
	for _, s := range cfg.Plugins {
		masked, paths := MaskSpecSecrets(s)
		out.Plugins = append(out.Plugins, masked)
		if len(paths) > 0 {
			secrets[s.Name] = paths
		}
	}
	return out, secrets
}

// UnmaskFromDisk merges an incoming (possibly masked) config from a PUT/POST
// payload with the authoritative on-disk values. Every SecretMask sentinel in
// `incoming` is replaced with the matching value from `onDisk`. New keys in
// `incoming` (not present on disk) pass through unchanged — they're treated as
// new secrets the operator typed in.
//
// Both arguments must be the same "kind" (both maps, both slices, both
// scalars) for descent to land — type mismatches stop descent at that level
// and `incoming` wins.
func UnmaskFromDisk(incoming, onDisk any) any {
	return unmaskWalk(incoming, onDisk)
}

// maskValue is the recursive walker behind MaskSpecSecrets. `secret` is true
// when an ancestor key already matched the pattern — once we're inside a
// secret subtree (e.g. "webhooks:") every scalar leaf below is masked.
func maskValue(v any, path string, secret bool, paths *[]string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			childPath := path + "." + k
			childSecret := secret || IsSecretKey(k)
			out[k] = maskValue(child, childPath, childSecret, paths)
		}
		return out
	case map[any]any:
		// YAML can decode non-string keys to interface{} keys when the source
		// declares them so; coerce to string keys for consistency with the
		// JSON wire format the server emits.
		out := make(map[string]any, len(x))
		for k, child := range x {
			ks := fmt.Sprintf("%v", k)
			childPath := path + "." + ks
			childSecret := secret || IsSecretKey(ks)
			out[ks] = maskValue(child, childPath, childSecret, paths)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			out[i] = maskValue(child, childPath, secret, paths)
		}
		return out
	default:
		// Scalar leaf. Mask only when we're in a secret subtree AND the value
		// is non-nil — masking a missing field would leak the fact that
		// there's a placeholder where there isn't really one.
		if secret && v != nil {
			*paths = append(*paths, path)
			return SecretMask
		}
		return v
	}
}

// unmaskWalk is the recursive merge that backs UnmaskFromDisk. When incoming
// and onDisk have the same shape, we descend and replace SecretMask leaves
// with their disk counterparts. On shape mismatch (different types at a
// path), incoming wins — interpreted as a deliberate change of shape, not a
// merge.
func unmaskWalk(incoming, onDisk any) any {
	switch in := incoming.(type) {
	case map[string]any:
		diskMap, _ := onDisk.(map[string]any)
		out := make(map[string]any, len(in))
		for k, v := range in {
			var d any
			if diskMap != nil {
				d = diskMap[k]
			}
			out[k] = unmaskWalk(v, d)
		}
		return out
	case map[any]any:
		// Same coercion as in maskValue — keep the shape consistent.
		diskMap, _ := onDisk.(map[string]any)
		out := make(map[string]any, len(in))
		for k, v := range in {
			ks := fmt.Sprintf("%v", k)
			var d any
			if diskMap != nil {
				d = diskMap[ks]
			}
			out[ks] = unmaskWalk(v, d)
		}
		return out
	case []any:
		diskSlice, _ := onDisk.([]any)
		out := make([]any, len(in))
		for i, v := range in {
			var d any
			if diskSlice != nil && i < len(diskSlice) {
				d = diskSlice[i]
			}
			out[i] = unmaskWalk(v, d)
		}
		return out
	case string:
		if in == SecretMask {
			return onDisk
		}
		return in
	default:
		return in
	}
}
