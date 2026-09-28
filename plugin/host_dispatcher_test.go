package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-mail/go-mail/cache"
)

var (
	hostCallerOnce sync.Once
	hostCallerBin  string
	hostCallerErr  error
)

// buildHostCaller lazily builds the host-caller test plugin.
// The binary lives outside the main module (own go.mod) so go test doesn't
// pull its dependencies into our own.
func buildHostCaller(t *testing.T) string {
	t.Helper()
	hostCallerOnce.Do(func() {
		outDir, err := os.MkdirTemp("", "host-caller-")
		if err != nil {
			hostCallerErr = err
			return
		}
		out := filepath.Join(outDir, "host-caller")
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = filepath.Join("testdata", "host-caller")
		if combined, berr := cmd.CombinedOutput(); berr != nil {
			hostCallerErr = &buildOutputErr{prefix: "go build host-caller", out: string(combined), err: berr}
			return
		}
		hostCallerBin = out
	})
	if hostCallerErr != nil {
		t.Fatalf("build host-caller: %v", hostCallerErr)
	}
	return hostCallerBin
}

type hostCallerOpts struct {
	HostCall        string // e.g. "host.list_accounts"
	HostCallPayload string // JSON
	HostMethods     string // comma-separated grant
}

// startManagerWithHostCaller boots a Manager + host-caller plugin configured
// via the supplied envvars + manifest grant. The store backs the HostAPI.
func startManagerWithHostCaller(t *testing.T, store *cache.Store, opts hostCallerOpts, qps int) *Manager {
	t.Helper()
	bin := buildHostCaller(t)
	envArgs := []string{
		"env",
		"HOST_CALL=" + opts.HostCall,
		"HOST_CALL_PAYLOAD=" + opts.HostCallPayload,
		"HOST_METHODS=" + opts.HostMethods,
	}
	envArgs = append(envArgs, bin)

	mgr := NewManager("go-mail/test")
	if err := mgr.Init(&Config{Plugins: []Spec{{
		Name:        "host-caller",
		Command:     envArgs,
		Timeout:     5 * time.Second,
		HostCallQPS: qps,
	}}}); err != nil {
		t.Fatalf("manager init: %v", err)
	}
	t.Cleanup(mgr.Shutdown)
	mgr.SetHostAPI(NewStoreHostAPI(store))
	return mgr
}

func evaluateOnce(t *testing.T, mgr *Manager) *EvaluateResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := mgr.Evaluate(ctx, "host-caller", EvaluateRequest{
		Email: &EvalEmail{UID: 1, Account: "a", Mailbox: "INBOX",
			Envelope: &cache.Envelope{Subject: "test"}},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	return resp
}

// TestHostRPC_ListAccountsEndToEnd: plugin makes a host.list_accounts call
// via the bidirectional reader path, gets back the configured store's data.
func TestHostRPC_ListAccountsEndToEnd(t *testing.T) {
	mgr := startManagerWithHostCaller(t, buildTestStore(), hostCallerOpts{
		HostCall:    "host.list_accounts",
		HostMethods: "list_accounts",
	}, 0)

	resp := evaluateOnce(t, mgr)
	if !strings.Contains(resp.Reason, `"name":"work"`) {
		t.Errorf("expected work account in reason, got: %s", resp.Reason)
	}
	if !strings.Contains(resp.Reason, `"name":"personal"`) {
		t.Errorf("expected personal account in reason, got: %s", resp.Reason)
	}
}

// TestHostRPC_ListMailboxesEndToEnd exercises a method with a payload argument.
func TestHostRPC_ListMailboxesEndToEnd(t *testing.T) {
	mgr := startManagerWithHostCaller(t, buildTestStore(), hostCallerOpts{
		HostCall:        "host.list_mailboxes",
		HostCallPayload: `{"account":"work"}`,
		HostMethods:     "list_mailboxes",
	}, 0)

	resp := evaluateOnce(t, mgr)
	if !strings.Contains(resp.Reason, `"name":"INBOX"`) {
		t.Errorf("expected INBOX in reason, got: %s", resp.Reason)
	}
	if !strings.Contains(resp.Reason, `"name":"Archive"`) {
		t.Errorf("expected Archive in reason, got: %s", resp.Reason)
	}
}

// TestHostRPC_GetEmailEndToEnd verifies the get_email method (with parts).
func TestHostRPC_GetEmailEndToEnd(t *testing.T) {
	mgr := startManagerWithHostCaller(t, buildTestStore(), hostCallerOpts{
		HostCall:        "host.get_email",
		HostCallPayload: `{"account":"work","mailbox":"INBOX","uid":1,"parts":["envelope","body"]}`,
		HostMethods:     "get_email",
	}, 0)

	resp := evaluateOnce(t, mgr)
	if !strings.Contains(resp.Reason, "Meeting") {
		t.Errorf("expected Meeting subject in reason, got: %s", resp.Reason)
	}
	if !strings.Contains(resp.Reason, "body for Meeting") {
		t.Errorf("expected body in reason, got: %s", resp.Reason)
	}
}

// TestHostRPC_DeniesUngrantedMethod: plugin tries a method it didn't declare
// in HostMethods. Dispatcher returns an "error: method not granted" response,
// which the host wraps as {"error": "..."}.
func TestHostRPC_DeniesUngrantedMethod(t *testing.T) {
	mgr := startManagerWithHostCaller(t, buildTestStore(), hostCallerOpts{
		HostCall:    "host.list_accounts",
		HostMethods: "list_mailboxes", // grants something else
	}, 0)

	resp := evaluateOnce(t, mgr)
	if !strings.Contains(resp.Reason, "method not granted") {
		t.Errorf("expected 'method not granted' in reason, got: %s", resp.Reason)
	}
}

// TestHostRPC_RateLimited: a tight burst exceeds the bucket and the dispatcher
// returns a structured {"error":"rate_limited", ...} payload.
func TestHostRPC_RateLimited(t *testing.T) {
	mgr := startManagerWithHostCaller(t, buildTestStore(), hostCallerOpts{
		HostCall:    "host.list_accounts",
		HostMethods: "list_accounts",
	}, 1) // burst of 1

	// First call should succeed.
	resp := evaluateOnce(t, mgr)
	if !strings.Contains(resp.Reason, `"name":"work"`) {
		t.Errorf("first call: expected accounts list, got: %s", resp.Reason)
	}

	// Immediate follow-up should be rate-limited.
	resp = evaluateOnce(t, mgr)
	if !strings.Contains(resp.Reason, "rate_limited") {
		t.Errorf("second call: expected rate_limited, got: %s", resp.Reason)
	}
	if !strings.Contains(resp.Reason, "retry_after_ms") {
		t.Errorf("second call: expected retry_after_ms field, got: %s", resp.Reason)
	}
}

// TestHostRPC_NoHostAPI: if the manager has no HostAPI installed, calls fail
// with "host API not configured".
func TestHostRPC_NoHostAPI(t *testing.T) {
	bin := buildHostCaller(t)
	envArgs := []string{
		"env",
		"HOST_CALL=host.list_accounts",
		"HOST_METHODS=list_accounts",
		bin,
	}
	mgr := NewManager("go-mail/test")
	if err := mgr.Init(&Config{Plugins: []Spec{{
		Name: "host-caller", Command: envArgs, Timeout: 5 * time.Second,
	}}}); err != nil {
		t.Fatalf("manager init: %v", err)
	}
	t.Cleanup(mgr.Shutdown)
	// Deliberately NOT calling SetHostAPI.

	resp := evaluateOnce(t, mgr)
	if !strings.Contains(resp.Reason, "host API not configured") {
		t.Errorf("expected 'host API not configured', got: %s", resp.Reason)
	}
}
