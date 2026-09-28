package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// pluginHostDispatcher routes one plugin's host.* requests through the
// authorization + rate-limit + dispatch chain. Created by the Manager once
// per Client and installed via Client.SetHostHandler.
//
// It implements HostHandler so the reader goroutine inside Client can call
// Dispatch directly. The first argument to Dispatch ("pluginName" in the
// HostHandler interface) is unused here because each dispatcher is already
// bound to a single plugin at construction time — that's the whole point of
// having one per plugin.
type pluginHostDispatcher struct {
	mgr        *Manager
	pluginName string
	limiter    *tokenBucket
}

// Dispatch satisfies HostHandler. Returns a response payload (possibly an
// error-shaped {"error": "..."} document) or a real Go error if something
// unrecoverable went wrong inside the dispatch chain.
func (d *pluginHostDispatcher) Dispatch(ctx context.Context, _, method string, payload json.RawMessage) (json.RawMessage, error) {
	// host.log is infrastructure, not a data method: no HostMethods grant, no
	// rate limit, no HostAPI dependency. Handled up front so it works for every
	// plugin (including external/dialed ones) regardless of manifest state.
	if method == MethodHostLog || method == "log" {
		return d.handleHostLog(payload)
	}

	client := d.mgr.Get(d.pluginName)
	if client == nil {
		return nil, fmt.Errorf("plugin %q not registered", d.pluginName)
	}
	manifest := client.Manifest()
	if !manifest.AllowsHostMethod(method) {
		return nil, fmt.Errorf("%w: %s", ErrHostMethodNotGranted, method)
	}

	d.mgr.hostMu.Lock()
	api := d.mgr.api
	d.mgr.hostMu.Unlock()
	if api == nil {
		return nil, errors.New("host API not configured")
	}

	if allowed, retry := d.limiter.Allow(); !allowed {
		payload, _ := json.Marshal(map[string]any{
			"error":          "rate_limited",
			"retry_after_ms": retry.Milliseconds(),
		})
		return payload, nil
	}

	method = strings.TrimPrefix(method, "host.")
	if method == "" {
		return nil, errors.New("missing method name")
	}
	return dispatchHostMethod(ctx, api, "host."+method, payload)
}

// handleHostLog re-emits a plugin's log line through the host's slog at the
// requested level, tagged with the plugin name. Returns an empty payload so
// the request/response round-trip completes; the plugin may ignore it.
func (d *pluginHostDispatcher) handleHostLog(payload json.RawMessage) (json.RawMessage, error) {
	var req HostLogRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("decode host.log: %w", err)
	}
	attrs := make([]any, 0, 2+2*len(req.Fields))
	attrs = append(attrs, "plugin", d.pluginName)
	for k, v := range req.Fields {
		attrs = append(attrs, k, v)
	}
	slog.Log(context.Background(), parsePluginLogLevel(req.Level), req.Msg, attrs...)
	return json.RawMessage(`{}`), nil
}

// parsePluginLogLevel maps a plugin-supplied level string to slog.Level,
// defaulting to Info for empty or unrecognized values.
func parsePluginLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// tokenBucket is a tiny per-plugin rate limiter for host.* calls.
//
// rate = qps tokens added per second; burst = qps. When the bucket empties,
// Allow returns false plus the wait duration until one token is available.
type tokenBucket struct {
	mu       sync.Mutex
	rate     float64 // tokens per second (== qps)
	capacity float64
	tokens   float64
	last     time.Time
	disabled bool // true when qps < 0 (unlimited)
}

func newTokenBucket(qps int) *tokenBucket {
	if qps < 0 {
		return &tokenBucket{disabled: true}
	}
	if qps == 0 {
		qps = 2 // default per README.md
	}
	r := float64(qps)
	return &tokenBucket{
		rate:     r,
		capacity: r,
		tokens:   r,
		last:     time.Now(),
	}
}

// Allow consumes one token if available. Returns (true, 0) on success or
// (false, retry-after duration) when the bucket is empty.
func (b *tokenBucket) Allow() (bool, time.Duration) {
	if b.disabled {
		return true, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	deficit := 1 - b.tokens
	return false, time.Duration(deficit / b.rate * float64(time.Second))
}
