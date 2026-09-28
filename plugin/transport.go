package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Transport carries NDJSON envelopes between go-mail and a plugin.
// Implementations are bidirectional and safe for concurrent WriteEnvelope callers.
// ReadEnvelope is expected to be driven by a single reader goroutine.
type Transport interface {
	ReadEnvelope() (*Envelope, error)
	WriteEnvelope(*Envelope) error
	Close() error
}

// newTransportFor selects and constructs the transport for a plugin spec.
// Returns the active transport; the caller owns Close.
func newTransportFor(spec Spec) (Transport, error) {
	switch spec.effectiveTransport() {
	case "stdio":
		return newStdioTransport(spec)
	case "socket":
		return newSocketTransport(spec)
	default:
		return nil, fmt.Errorf("unknown transport %q", spec.Transport)
	}
}

// stdioTransport owns a child process whose stdin/stdout carries the NDJSON stream.
// Stderr is forwarded to slog at Info level.
type stdioTransport struct {
	name string
	cmd  *exec.Cmd

	stdin  io.WriteCloser
	stdout *bufio.Reader

	writeMu sync.Mutex

	closeOnce sync.Once
	closed    chan struct{}
}

// newStdioTransport spawns the plugin process and returns a transport bound to its stdio.
// If spawn fails, no process is left running.
func newStdioTransport(spec Spec) (*stdioTransport, error) {
	if len(spec.Command) == 0 {
		return nil, errors.New("plugin command is empty")
	}

	cmd := exec.Command(spec.Command[0], spec.Command[1:]...) //nolint:gosec // commands come from operator-controlled config

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start plugin: %w", err)
	}

	t := &stdioTransport{
		name:   spec.Name,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdout, 1<<20),
		closed: make(chan struct{}),
	}

	go forwardStderr(spec.Name, stderr)

	return t, nil
}

// ReadEnvelope reads one NDJSON line and decodes it into an envelope.
// Returns io.EOF when the plugin's stdout is closed.
func (t *stdioTransport) ReadEnvelope() (*Envelope, error) {
	line, err := t.stdout.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	return &env, nil
}

// WriteEnvelope is safe for concurrent callers.
func (t *stdioTransport) WriteEnvelope(env *Envelope) error {
	line, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write to plugin: %w", err)
	}
	return nil
}

// Close shuts down the transport and the child process.
// Closing stdin signals EOF; the process gets a brief grace period to exit
// before SIGKILL.
func (t *stdioTransport) Close() error {
	var closeErr error
	t.closeOnce.Do(func() {
		if t.stdin != nil {
			_ = t.stdin.Close()
		}
		if t.cmd != nil && t.cmd.Process != nil {
			done := make(chan error, 1)
			go func() { done <- t.cmd.Wait() }()
			select {
			case <-time.After(500 * time.Millisecond):
				_ = t.cmd.Process.Kill()
				<-done
			case <-done:
			}
		}
		close(t.closed)
	})
	return closeErr
}

func forwardStderr(name string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		slog.Info("plugin log", "plugin", name, "msg", scanner.Text())
	}
}

// socketTransport carries NDJSON over a net.Conn. When proc is non-nil this
// connection holds a ref on a shared (spawned) plugin process; Close releases
// it, and the process is torn down once its last connection detaches.
type socketTransport struct {
	name string
	conn net.Conn

	proc *sharedProc // non-nil for spawned plugins; nil for external/dialed ones

	reader *bufio.Reader

	writeMu sync.Mutex

	closeOnce sync.Once
}

// newSocketTransport opens a transport based on the spec.
//
// Spawn mode (spec.Command non-empty): acquire the shared process for this
// binary (spawning it once if needed), then dial a fresh connection to its
// socket. Specs sharing a command reuse one OS process but each get their own
// connection and init handshake.
//
// External mode (spec.Address set, spec.Command empty): dial the operator-
// provided address. Schemes: unix:///path (TCP not yet implemented).
func newSocketTransport(spec Spec) (*socketTransport, error) {
	switch {
	case len(spec.Command) > 0:
		return spawnSocketTransport(spec)
	case spec.Address != "":
		return dialExternalSocketTransport(spec)
	default:
		return nil, errors.New("socket transport requires either command or address")
	}
}

// spawnSocketTransport attaches to the shared process for spec's binary and
// dials its own connection. The process is spawned only once per binary; this
// connection's Close releases the ref.
func spawnSocketTransport(spec Spec) (*socketTransport, error) {
	dialTO := spec.DialTimeout
	if dialTO <= 0 {
		dialTO = 5 * time.Second
	}

	proc, err := sharedProcs.acquire(spec)
	if err != nil {
		return nil, err
	}

	conn, dialErr := net.DialTimeout("unix", proc.socketPath, dialTO)
	if dialErr != nil {
		sharedProcs.release(proc)
		return nil, fmt.Errorf("dial plugin socket: %w", dialErr)
	}

	return &socketTransport{
		name:   spec.Name,
		conn:   conn,
		proc:   proc,
		reader: bufio.NewReaderSize(conn, 1<<20),
	}, nil
}

func dialExternalSocketTransport(spec Spec) (*socketTransport, error) {
	dialTO := spec.DialTimeout
	if dialTO <= 0 {
		dialTO = 5 * time.Second
	}
	network, addr, err := parseAddress(spec.Address)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: dialTO}
	conn, err := dialer.DialContext(context.Background(), network, addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", spec.Address, err)
	}
	return &socketTransport{
		name:   spec.Name,
		conn:   conn,
		reader: bufio.NewReaderSize(conn, 1<<20),
	}, nil
}

func (t *socketTransport) ReadEnvelope() (*Envelope, error) {
	line, err := t.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	return &env, nil
}

func (t *socketTransport) WriteEnvelope(env *Envelope) error {
	line, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write to plugin: %w", err)
	}
	return nil
}

// Close closes this connection and releases its hold on the shared process.
// The process itself is torn down by the registry once its last connection
// detaches; external (dialed) transports have no proc and just close the conn.
func (t *socketTransport) Close() error {
	t.closeOnce.Do(func() {
		if t.conn != nil {
			_ = t.conn.Close()
		}
		if t.proc != nil {
			sharedProcs.release(t.proc)
		}
	})
	return nil
}

// parseAddress accepts "unix:///path" (TCP forms reserved for later) and
// returns the net.Dial network+address.
func parseAddress(addr string) (network string, address string, err error) {
	u, perr := url.Parse(addr)
	if perr != nil {
		return "", "", fmt.Errorf("parse address %q: %w", addr, perr)
	}
	switch u.Scheme {
	case "unix":
		// unix:///path → u.Path = "/path"
		if u.Path == "" {
			return "", "", fmt.Errorf("address %q: missing path", addr)
		}
		return "unix", u.Path, nil
	case "tcp", "tcp4", "tcp6":
		return "", "", fmt.Errorf("address scheme %q: TCP transport not yet implemented", u.Scheme)
	default:
		// Permit a bare unix path with no scheme as a convenience.
		if strings.HasPrefix(addr, "/") {
			return "unix", addr, nil
		}
		return "", "", fmt.Errorf("address %q: unknown scheme", addr)
	}
}

// ctxDeadline is a small helper used by Client when picking a per-request deadline.
func ctxDeadline(ctx context.Context, fallback time.Duration) time.Duration {
	if ctx == nil {
		return fallback
	}
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 && d < fallback {
			return d
		}
	}
	return fallback
}
