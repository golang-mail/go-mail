package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// procKey identifies a spawnable plugin binary. Specs sharing a procKey share
// one OS process; each spec still opens its own socket connection (and thus its
// own init handshake / config), so per-spec config can diverge while the
// process is spawned only once.
type procKey string

// procKeyFor derives the shared-process identity from a spec's full command.
// Keyed on the whole command (binary + args) so identical invocations collapse
// to one process while differing args stay distinct.
func procKeyFor(spec Spec) procKey {
	return procKey("socket\x00" + strings.Join(spec.Command, "\x00"))
}

// sharedProc is one OS process serving one or more plugin connections.
type sharedProc struct {
	key        procKey
	cmd        *exec.Cmd
	socketPath string
	reg        *procRegistry

	refs   int           // active connections; guarded by reg.mu
	exited bool          // set by wait() once cmd exits; guarded by reg.mu
	done   chan struct{} // closed after cmd.Wait returns and cleanup completes
}

// procRegistry tracks live shared processes keyed by procKey.
type procRegistry struct {
	mu    sync.Mutex
	procs map[procKey]*sharedProc
}

func newProcRegistry() *procRegistry {
	return &procRegistry{procs: make(map[procKey]*sharedProc)}
}

// sharedProcs dedups spawned socket plugins to one OS process per binary.
var sharedProcs = newProcRegistry()

// acquire returns a running shared process for spec, spawning one if none is
// alive for its procKey, and bumps the refcount. The caller dials the returned
// socketPath for its own connection and must call release exactly once when
// that connection closes.
func (r *procRegistry) acquire(spec Spec) (*sharedProc, error) {
	key := procKeyFor(spec)
	r.mu.Lock()
	defer r.mu.Unlock()

	if p := r.procs[key]; p != nil && !p.exited {
		p.refs++
		return p, nil
	}

	p, err := r.spawnLocked(spec, key)
	if err != nil {
		return nil, err
	}
	r.procs[key] = p
	p.refs = 1
	go p.wait()
	return p, nil
}

// spawnLocked starts the process and waits for its socket to appear. Caller
// holds r.mu. On any failure the child is killed so nothing leaks.
func (r *procRegistry) spawnLocked(spec Spec, key procKey) (*sharedProc, error) {
	dialTO := spec.DialTimeout
	if dialTO <= 0 {
		dialTO = 5 * time.Second
	}

	path, err := allocateSharedSocketPath(spec.Command)
	if err != nil {
		return nil, fmt.Errorf("allocate socket path: %w", err)
	}
	_ = os.Remove(path) // clear any stale socket from a crashed run

	cmd := exec.Command(spec.Command[0], spec.Command[1:]...) //nolint:gosec // operator-controlled
	cmd.Env = append(os.Environ(), "GO_MAIL_PLUGIN_SOCKET="+path)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start plugin: %w", err)
	}

	go forwardStderr(filepath.Base(spec.Command[0]), stderr)

	if err := waitForSocket(path, dialTO); err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = os.Remove(path)
		return nil, fmt.Errorf("wait for plugin socket: %w", err)
	}

	slog.Info("plugin process spawned", "binary", spec.Command[0], "socket", path)
	return &sharedProc{
		key:        key,
		cmd:        cmd,
		socketPath: path,
		reg:        r,
		done:       make(chan struct{}),
	}, nil
}

// release drops one connection's hold on p. When the last connection detaches,
// the process is signalled to stop. Matching by pointer guards against a
// respawned entry under the same key being decremented by a stale connection.
func (r *procRegistry) release(p *sharedProc) {
	r.mu.Lock()
	p.refs--
	last := p.refs <= 0
	if last && r.procs[p.key] == p {
		delete(r.procs, p.key)
	}
	r.mu.Unlock()
	if last {
		p.stop()
	}
}

// wait reaps the process. On exit it marks the entry dead and removes it from
// the registry so the next acquire respawns rather than handing out a corpse.
func (p *sharedProc) wait() {
	_ = p.cmd.Wait()
	p.reg.mu.Lock()
	p.exited = true
	if p.reg.procs[p.key] == p {
		delete(p.reg.procs, p.key)
	}
	p.reg.mu.Unlock()
	_ = os.Remove(p.socketPath)
	close(p.done)
}

// stop signals the process to exit, escalating to SIGKILL after a grace period.
// Safe to call after the process already exited (done is already closed).
func (p *sharedProc) stop() {
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(500 * time.Millisecond):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// waitForSocket blocks until the plugin's socket file appears or the timeout
// elapses. Listener creation (net.Listen) makes the file and starts accepting
// in one step, so file existence is a sufficient readiness signal.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat socket: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for socket at %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// allocateSharedSocketPath returns a deterministic per-binary socket path so
// every spec that shares a process dials the same socket. The name combines the
// executable basename (readability) with a hash of the full command (uniqueness
// across differing args).
func allocateSharedSocketPath(command []string) (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = filepath.Join(os.TempDir(), "go-mail-"+strconv.Itoa(os.Getuid()))
	}
	dir := filepath.Join(base, "go-mail", "plugins")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o700)

	sum := sha256.Sum256([]byte(strings.Join(command, "\x00")))
	name := filepath.Base(command[0]) + "-" + hex.EncodeToString(sum[:])[:12]
	return filepath.Join(dir, name+".sock"), nil
}
