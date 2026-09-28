package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Spec is the static configuration for a plugin (one entry in plugins.yml).
// Config is decoded as an untyped value from YAML and re-serialized to JSON
// when sent to the plugin's init handshake — plugin authors get JSON regardless
// of how the operator wrote the config.
type Spec struct {
	Name    string   `yaml:"name"`
	Command []string `yaml:"command"`
	// Transport selects the wire transport: "socket" (default) or "stdio".
	// Socket plugins listen on a Unix socket whose path go-mail provides via
	// GO_MAIL_PLUGIN_SOCKET; stdio plugins talk NDJSON over the child's stdio.
	Transport string `yaml:"transport,omitempty"`
	// Address is only meaningful for socket transport. Set it for external
	// plugins (no `command:`) that go-mail should dial but not spawn.
	// Schemes: "unix:///path", "tcp://host:port" (TCP not yet implemented).
	Address     string        `yaml:"address,omitempty"`
	DialTimeout time.Duration `yaml:"dial_timeout,omitempty"`
	Timeout     time.Duration `yaml:"timeout,omitempty"`
	MaxRestarts int           `yaml:"max_restarts,omitempty"`
	// HostCallQPS caps how many host.* RPC requests this plugin may issue per
	// second (token-bucket; burst = QPS). Defaults to 2 when unset. Set to a
	// negative value to disable the limit entirely.
	HostCallQPS int `yaml:"host_call_qps,omitempty"`
	Config      any `yaml:"config,omitempty"`
	// Disabled, when true, tells the Manager to keep the spec known but skip
	// spawning a child process. Distinct from Client.disabled (the supervisor
	// backoff flag set after MaxRestarts) — operator-controlled, persists in
	// plugins.yml. Toggleable at runtime via Manager.ApplyConfig.
	Disabled bool `yaml:"disabled,omitempty"`
}

// effectiveTransport returns the transport this spec resolves to, applying defaults.
func (s Spec) effectiveTransport() string {
	switch s.Transport {
	case "stdio", "socket":
		return s.Transport
	}
	return "socket"
}

// Status is a snapshot of a plugin client's state, suitable for the /api/plugins endpoint.
type Status struct {
	Name        string    `json:"name"`
	Running     bool      `json:"running"`
	Disabled    bool      `json:"disabled"`
	Restarts    int       `json:"restarts"`
	LastError   string    `json:"last_error,omitempty"`
	LastStarted time.Time `json:"last_started"`
	Manifest    *Manifest `json:"manifest,omitempty"`
}

// pendingResp is the value delivered by the reader goroutine to a waiting exchange().
type pendingResp struct {
	payload json.RawMessage
	err     error
}

// Client is one supervised plugin process.
//
// Internally a long-lived reader goroutine demultiplexes incoming envelopes:
// type="response" envelopes resolve in-flight host→plugin requests (matched by id),
// type="request" envelopes are dispatched to the configured HostHandler so the
// plugin can ask the host for things, type="event" envelopes are logged.
//
// callMu serializes Evaluate calls so only one is in flight at a time — keeps
// the "concurrency: serial" decision until a concurrency knob lands. The
// reader/demuxer can handle concurrent in-flight requests fine; this lock is
// just a policy guard.
type Client struct {
	spec        Spec
	hostVersion string

	mu          sync.Mutex // guards transport, manifest, hostHandler
	transport   Transport
	manifest    *Manifest
	hostHandler HostHandler

	inflightMu sync.Mutex
	inflight   map[string]chan pendingResp

	callMu sync.Mutex // serializes Evaluate

	disabled    atomic.Bool
	running     atomic.Bool
	restarts    atomic.Int32
	lastErr     atomic.Value // string
	lastStarted atomic.Value // time.Time
}

// NewClient builds a client. Call Start to spawn the process.
func NewClient(spec Spec, hostVersion string) *Client {
	if spec.Timeout <= 0 {
		spec.Timeout = 5 * time.Second
	}
	if spec.MaxRestarts <= 0 {
		spec.MaxRestarts = 5
	}
	return &Client{
		spec:        spec,
		hostVersion: hostVersion,
		hostHandler: nullHostHandler{},
		inflight:    make(map[string]chan pendingResp),
	}
}

// SetHostHandler installs the dispatcher for plugin-initiated host.* calls.
// Defaults to a handler that refuses everything; the real backing implementation
// lands with the host-RPC chunk.
func (c *Client) SetHostHandler(h HostHandler) {
	if h == nil {
		h = nullHostHandler{}
	}
	c.mu.Lock()
	c.hostHandler = h
	c.mu.Unlock()
}

// Start spawns the plugin process and performs the init handshake.
func (c *Client) Start() error {
	if c.disabled.Load() {
		return errors.New("plugin disabled")
	}

	c.mu.Lock()
	if c.running.Load() {
		c.mu.Unlock()
		return nil
	}

	t, err := newTransportFor(c.spec)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	c.transport = t
	c.mu.Unlock()

	c.inflightMu.Lock()
	c.inflight = make(map[string]chan pendingResp)
	c.inflightMu.Unlock()

	c.running.Store(true)
	c.lastStarted.Store(time.Now())

	go c.readLoop(t)

	manifest, err := c.handshake()
	if err != nil {
		c.mu.Lock()
		if c.transport != nil {
			_ = c.transport.Close()
			c.transport = nil
		}
		c.mu.Unlock()
		c.running.Store(false)
		return fmt.Errorf("init handshake: %w", err)
	}

	c.mu.Lock()
	c.manifest = manifest
	c.mu.Unlock()

	slog.Info("plugin started",
		"name", c.spec.Name,
		"manifest_name", manifest.Name,
		"version", manifest.Version,
		"wants", manifest.Wants,
		"cacheable", manifest.Cacheable,
	)
	return nil
}

func (c *Client) handshake() (*Manifest, error) {
	var configJSON json.RawMessage
	if c.spec.Config != nil {
		raw, err := json.Marshal(c.spec.Config)
		if err != nil {
			return nil, fmt.Errorf("marshal plugin config: %w", err)
		}
		configJSON = raw
	}
	req := InitRequest{HostVersion: c.hostVersion, Config: configJSON}
	payload, _ := json.Marshal(req)

	ctx, cancel := context.WithTimeout(context.Background(), c.spec.Timeout)
	defer cancel()

	resp, err := c.exchange(ctx, "init", payload)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(resp, &m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Name == "" {
		m.Name = c.spec.Name
	}
	return &m, nil
}

// Evaluate sends an evaluate request and returns the response.
// On transport-level error the process is torn down so the next call can respawn.
func (c *Client) Evaluate(ctx context.Context, req EvaluateRequest) (*EvaluateResponse, error) {
	if c.disabled.Load() {
		return nil, errors.New("plugin disabled")
	}

	c.callMu.Lock()
	defer c.callMu.Unlock()

	if !c.running.Load() {
		if err := c.Start(); err != nil {
			c.recordRestartFailure(err)
			return nil, err
		}
	}

	manifest := c.Manifest()
	if req.Email != nil && manifest != nil {
		wants := map[string]bool{}
		for _, w := range manifest.Wants {
			wants[w] = true
		}
		if !wants["body"] {
			req.Email.Body = ""
			req.Email.ContentType = ""
		}
		if !wants["headers"] {
			req.Email.Headers = nil
		}
		if !wants["envelope"] {
			req.Email.Envelope = nil
		}
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal evaluate: %w", err)
	}

	respPayload, err := c.exchange(ctx, "evaluate", payload)
	if err != nil {
		c.tearDown(err)
		return nil, err
	}

	var resp EvaluateResponse
	if err := json.Unmarshal(respPayload, &resp); err != nil {
		return nil, fmt.Errorf("decode evaluate response: %w", err)
	}
	if resp.Error != "" {
		return &resp, fmt.Errorf("plugin reported error: %s", resp.Error)
	}
	return &resp, nil
}

// exchange registers an in-flight slot, writes the request envelope, and waits
// for the matching response (delivered by the reader goroutine) or a timeout.
func (c *Client) exchange(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	t := c.transport
	c.mu.Unlock()
	if t == nil {
		return nil, errors.New("plugin not running")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	id := uuid.NewString()
	ch := make(chan pendingResp, 1)

	c.inflightMu.Lock()
	c.inflight[id] = ch
	c.inflightMu.Unlock()
	defer func() {
		c.inflightMu.Lock()
		delete(c.inflight, id)
		c.inflightMu.Unlock()
	}()

	env := &Envelope{ID: id, Type: "request", Method: method, Payload: payload}
	if err := t.WriteEnvelope(env); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	timeout := ctxDeadline(ctx, c.spec.Timeout)
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil, fmt.Errorf("plugin %s timed out after %s", c.spec.Name, timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.payload, nil
	}
}

// readLoop drains envelopes from the plugin until the transport closes.
// On exit it fails all in-flight requests with the read error.
func (c *Client) readLoop(t Transport) {
	for {
		env, err := t.ReadEnvelope()
		if err != nil {
			c.handleReaderExit(err)
			return
		}
		switch env.Type {
		case "response":
			c.deliverResponse(env)
		case "request":
			go c.serveHostRequest(env)
		case "event":
			slog.Debug("plugin event", "plugin", c.spec.Name, "method", env.Method)
		default:
			slog.Warn("plugin sent unknown envelope type", "plugin", c.spec.Name, "type", env.Type, "method", env.Method)
		}
	}
}

func (c *Client) deliverResponse(env *Envelope) {
	c.inflightMu.Lock()
	ch, ok := c.inflight[env.ID]
	if ok {
		delete(c.inflight, env.ID)
	}
	c.inflightMu.Unlock()
	if !ok {
		slog.Warn("plugin response for unknown id", "plugin", c.spec.Name, "id", env.ID, "method", env.Method)
		return
	}
	ch <- pendingResp{payload: env.Payload}
}

func (c *Client) serveHostRequest(env *Envelope) {
	c.mu.Lock()
	handler := c.hostHandler
	t := c.transport
	c.mu.Unlock()
	if handler == nil {
		handler = nullHostHandler{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.spec.Timeout)
	defer cancel()

	out := &Envelope{ID: env.ID, Type: "response", Method: env.Method}
	resp, err := handler.Dispatch(ctx, c.spec.Name, env.Method, env.Payload)
	if err != nil {
		errPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		out.Payload = errPayload
	} else {
		out.Payload = resp
	}

	if t == nil {
		return
	}
	if werr := t.WriteEnvelope(out); werr != nil {
		slog.Warn("write host-rpc response", "plugin", c.spec.Name, "method", env.Method, "err", werr)
	}
}

// handleReaderExit is called once when the reader goroutine stops.
// It tears down transport state and unblocks in-flight callers.
// Restart counting happens in the call site (Evaluate / Start) rather than here
// so we don't double-increment when a caller already noticed the failure.
//
// A "clean" exit covers three shapes:
//   - io.EOF / io.ErrClosedPipe   — peer (plugin) closed the stream cleanly
//   - net.ErrClosed                — we closed the socket locally (Shutdown,
//     RestartPlugin, ApplyConfig diff). The reader's blocked ReadEnvelope
//     wakes with "use of closed network connection"; that is the wake-up
//     signal, not an anomaly.
//
// Anything else is genuinely unexpected and worth a WARN.
func (c *Client) handleReaderExit(err error) {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		slog.Debug("plugin reader exited cleanly", "plugin", c.spec.Name, "err", err)
	} else {
		slog.Warn("plugin reader exited", "plugin", c.spec.Name, "err", err)
	}

	c.inflightMu.Lock()
	pending := c.inflight
	c.inflight = make(map[string]chan pendingResp)
	c.inflightMu.Unlock()

	exitErr := fmt.Errorf("plugin %s connection closed: %w", c.spec.Name, err)
	for _, ch := range pending {
		select {
		case ch <- pendingResp{err: exitErr}:
		default:
		}
	}

	c.mu.Lock()
	if c.transport != nil {
		_ = c.transport.Close()
		c.transport = nil
	}
	c.mu.Unlock()
	c.running.Store(false)
}

// tearDown closes the transport and records a restart failure.
// Used by callers that already observed an error (e.g. Evaluate after exchange failed).
func (c *Client) tearDown(reason error) {
	c.mu.Lock()
	t := c.transport
	c.transport = nil
	c.mu.Unlock()
	if t != nil {
		_ = t.Close()
	}
	c.running.Store(false)
	c.recordRestartFailure(reason)
}

// SendEvent fires a one-way envelope (type="event") at the plugin.
// No id is allocated, no response is expected. Used for batch_complete and
// future broadcast-style notifications.
//
// Returns an error only if the plugin isn't running or the write itself fails.
// Plugins that don't recognize the method should ignore it silently.
func (c *Client) SendEvent(method string, payload json.RawMessage) error {
	if c.disabled.Load() {
		return errors.New("plugin disabled")
	}
	c.mu.Lock()
	t := c.transport
	c.mu.Unlock()
	if t == nil {
		return errors.New("plugin not running")
	}
	env := &Envelope{ID: uuid.NewString(), Type: "event", Method: method, Payload: payload}
	if err := t.WriteEnvelope(env); err != nil {
		return fmt.Errorf("write event %s: %w", method, err)
	}
	return nil
}

// Shutdown sends a shutdown notice and tears down the transport + process.
func (c *Client) Shutdown() {
	if !c.running.Load() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, _ = c.exchange(ctx, "shutdown", json.RawMessage(`{}`))

	c.mu.Lock()
	t := c.transport
	c.transport = nil
	c.mu.Unlock()
	if t != nil {
		_ = t.Close()
	}
	c.running.Store(false)
}

func (c *Client) recordRestartFailure(err error) {
	c.lastErr.Store(err.Error())
	n := c.restarts.Add(1)
	if int(n) >= c.spec.MaxRestarts {
		slog.Error("plugin disabled after repeated failures", "name", c.spec.Name, "restarts", n, "max", c.spec.MaxRestarts)
		c.disabled.Store(true)
	}
}

// Status returns a snapshot of plugin state.
func (c *Client) Status() Status {
	s := Status{
		Name:     c.spec.Name,
		Running:  c.running.Load(),
		Disabled: c.disabled.Load(),
		Restarts: int(c.restarts.Load()),
	}
	if v, ok := c.lastErr.Load().(string); ok {
		s.LastError = v
	}
	if v, ok := c.lastStarted.Load().(time.Time); ok {
		s.LastStarted = v
	}
	c.mu.Lock()
	if c.manifest != nil {
		m := *c.manifest
		s.Manifest = &m
	}
	c.mu.Unlock()
	return s
}

// Manifest returns the manifest reported during init, or nil if not yet started.
func (c *Client) Manifest() *Manifest {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.manifest == nil {
		return nil
	}
	m := *c.manifest
	return &m
}
