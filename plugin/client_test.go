package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

const subjectNoopBin = "/workspace/plugins/subject-noop/subject-noop"

var (
	stdioNoopOnce sync.Once
	stdioNoopBin  string
	stdioNoopErr  error
)

// buildStdioNoop lazily builds the test-only stdio plugin under testdata/stdio-noop.
// Built into a temp dir that persists for the whole `go test` run.
func buildStdioNoop(t *testing.T) string {
	t.Helper()
	stdioNoopOnce.Do(func() {
		outDir, err := os.MkdirTemp("", "stdio-noop-")
		if err != nil {
			stdioNoopErr = err
			return
		}
		out := filepath.Join(outDir, "stdio-noop")
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = filepath.Join("testdata", "stdio-noop")
		if combined, berr := cmd.CombinedOutput(); berr != nil {
			stdioNoopErr = fmt.Errorf("go build stdio-noop: %w\n%s", berr, combined)
			return
		}
		stdioNoopBin = out
	})
	if stdioNoopErr != nil {
		t.Fatalf("build stdio-noop: %v", stdioNoopErr)
	}
	return stdioNoopBin
}

func ensureNoopAvailable(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(subjectNoopBin); err != nil {
		t.Skipf("subject-noop binary not built at %s: %v", subjectNoopBin, err)
	}
}

// newTestClient starts a socket-mode subject-noop client (the default transport).
func newTestClient(t *testing.T) *Client {
	t.Helper()
	ensureNoopAvailable(t)
	c := NewClient(Spec{
		Name:    "subject-noop",
		Command: []string{subjectNoopBin},
		Timeout: 5 * time.Second,
	}, "go-mail/test")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Shutdown)
	return c
}

// newStdioTestClient starts a stdio-mode test plugin (built from testdata).
func newStdioTestClient(t *testing.T) *Client {
	t.Helper()
	bin := buildStdioNoop(t)
	c := NewClient(Spec{
		Name:      "stdio-noop",
		Command:   []string{bin},
		Transport: "stdio",
		Timeout:   5 * time.Second,
	}, "go-mail/test")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Shutdown)
	return c
}

func TestClient_InitAndEvaluate(t *testing.T) {
	c := newTestClient(t)

	m := c.Manifest()
	if m == nil {
		t.Fatal("manifest nil after Start")
	}
	if m.Name != "subject-noop" {
		t.Errorf("manifest.Name = %q, want subject-noop", m.Name)
	}
	if m.Version == "" {
		t.Errorf("manifest.Version empty")
	}

	req := EvaluateRequest{
		Email: &EvalEmail{
			UID:     1,
			Account: "acct",
			Mailbox: "INBOX",
			Envelope: &cache.Envelope{
				Subject: "hello",
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := c.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	if resp.Matched || len(resp.Actions) != 0 || resp.Error != "" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

// TestClient_ConcurrentEvaluatesSerialize confirms that even with multiple
// callers, evaluates complete successfully (callMu serializes them, the
// bidirectional reader still demuxes responses by id).
func TestClient_ConcurrentEvaluatesSerialize(t *testing.T) {
	c := newTestClient(t)

	const N = 10
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := c.Evaluate(ctx, EvaluateRequest{
				Email: &EvalEmail{UID: uint32(i + 1), Account: "a", Mailbox: "INBOX",
					Envelope: &cache.Envelope{Subject: "s"}},
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("evaluate err: %v", err)
		}
	}
}

// TestClient_StdioTransport exercises the stdio transport explicitly so we
// keep both transports covered even though socket is the default.
func TestClient_StdioTransport(t *testing.T) {
	c := newStdioTestClient(t)

	m := c.Manifest()
	if m == nil {
		t.Fatal("manifest nil")
	}
	if m.Name != "stdio-noop" {
		t.Errorf("manifest.Name = %q, want stdio-noop", m.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := c.Evaluate(ctx, EvaluateRequest{
		Email: &EvalEmail{UID: 1, Account: "a", Mailbox: "INBOX",
			Envelope: &cache.Envelope{Subject: "hi"}},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if resp.Matched || len(resp.Actions) != 0 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

// TestClient_HostRequestRefused verifies the null HostHandler refuses any
// host.* method call cleanly with an error.
func TestClient_HostRequestRefused(t *testing.T) {
	resp, err := (nullHostHandler{}).Dispatch(context.Background(), "x", "host.list_mailboxes", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error from nullHostHandler")
	}
	if resp != nil {
		t.Errorf("expected nil payload, got %s", resp)
	}
}

// TestSpec_DefaultTransport documents the default-transport behavior.
func TestSpec_DefaultTransport(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "socket"},
		{"socket", "socket"},
		{"stdio", "stdio"},
		{"bogus", "socket"},
	}
	for _, tc := range cases {
		got := Spec{Transport: tc.in}.effectiveTransport()
		if got != tc.want {
			t.Errorf("effectiveTransport(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// evalOnce drives a single evaluate over c's own connection; helper for the
// shared-process tests.
func evalOnce(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Evaluate(ctx, EvaluateRequest{Email: &EvalEmail{
		UID: 1, Account: "acct", Mailbox: "INBOX",
		Envelope: &cache.Envelope{Subject: "hi"},
	}}); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
}

func procRefs(key procKey) (refs int, present bool) {
	sharedProcs.mu.Lock()
	defer sharedProcs.mu.Unlock()
	p, ok := sharedProcs.procs[key]
	if !ok {
		return 0, false
	}
	return p.refs, true
}

// TestSharedProcess_OneBinaryOneProcess: two specs sharing a command spawn one
// OS process with two connections; detaching one keeps it alive, detaching the
// last tears it down.
func TestSharedProcess_OneBinaryOneProcess(t *testing.T) {
	ensureNoopAvailable(t)
	mkSpec := func(name string) Spec {
		return Spec{Name: name, Command: []string{subjectNoopBin}, Timeout: 5 * time.Second}
	}
	key := procKeyFor(mkSpec("x"))

	c1 := NewClient(mkSpec("noop-a"), "go-mail/test")
	if err := c1.Start(); err != nil {
		t.Fatalf("start c1: %v", err)
	}
	c2 := NewClient(mkSpec("noop-b"), "go-mail/test")
	if err := c2.Start(); err != nil {
		c1.Shutdown()
		t.Fatalf("start c2: %v", err)
	}

	if refs, ok := procRefs(key); !ok || refs != 2 {
		c1.Shutdown()
		c2.Shutdown()
		t.Fatalf("shared refs = %d present=%v, want 2/true", refs, ok)
	}

	// Both connections are independently usable.
	evalOnce(t, c1)
	evalOnce(t, c2)

	// Detaching one keeps the process alive for the other.
	c1.Shutdown()
	if refs, ok := procRefs(key); !ok || refs != 1 {
		c2.Shutdown()
		t.Fatalf("refs after one shutdown = %d present=%v, want 1/true", refs, ok)
	}
	evalOnce(t, c2)

	// Detaching the last tears the process down. stop() blocks on exit, so the
	// entry is gone by the time Shutdown returns.
	c2.Shutdown()
	if refs, ok := procRefs(key); ok {
		t.Fatalf("process still present after last shutdown: refs=%d", refs)
	}
}

// TestSharedProcess_RespawnsAfterCrash: if the shared process dies, the entry
// is reaped and the next evaluate respawns a fresh one.
func TestSharedProcess_RespawnsAfterCrash(t *testing.T) {
	ensureNoopAvailable(t)
	spec := Spec{Name: "noop-crash", Command: []string{subjectNoopBin}, Timeout: 5 * time.Second}
	key := procKeyFor(spec)

	c := NewClient(spec, "go-mail/test")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Shutdown)

	sharedProcs.mu.Lock()
	p := sharedProcs.procs[key]
	sharedProcs.mu.Unlock()
	if p == nil {
		t.Fatal("no shared process registered")
	}
	oldPID := p.cmd.Process.Pid

	_ = p.cmd.Process.Kill()
	<-p.done // wait() observed exit and removed the dead entry

	if _, ok := procRefs(key); ok {
		t.Fatal("dead entry not reaped from registry")
	}

	// Let the client's reader observe the broken connection and mark itself
	// not-running, as it would between filter passes in production.
	deadline := time.Now().Add(2 * time.Second)
	for c.running.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// Next evaluate respawns transparently.
	evalOnce(t, c)

	sharedProcs.mu.Lock()
	p2 := sharedProcs.procs[key]
	sharedProcs.mu.Unlock()
	if p2 == nil {
		t.Fatal("no process after respawn")
	}
	if p2.cmd.Process.Pid == oldPID {
		t.Errorf("expected new pid after respawn, still %d", oldPID)
	}
}
