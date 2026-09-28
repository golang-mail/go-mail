package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestLimiter(start time.Time) (*loginLimiter, *time.Time) {
	now := start
	l := newLoginLimiter()
	l.now = func() time.Time { return now }
	return l, &now
}

func TestLoginLimiter_BlocksAfterFreeFailures(t *testing.T) {
	l, _ := newTestLimiter(time.Unix(1000, 0))
	for i := 0; i < loginFreeFailures; i++ {
		if d := l.retryAfter("1.2.3.4"); d != 0 {
			t.Fatalf("failure %d: want allowed, got retry after %v", i, d)
		}
		l.fail("1.2.3.4")
	}
	if d := l.retryAfter("1.2.3.4"); d <= 0 {
		t.Fatalf("want blocked after %d failures, got allowed", loginFreeFailures)
	}
	if d := l.retryAfter("5.6.7.8"); d != 0 {
		t.Fatalf("other IP must not be affected, got retry after %v", d)
	}
}

func TestLoginLimiter_BackoffDoublesAndCaps(t *testing.T) {
	l, now := newTestLimiter(time.Unix(1000, 0))
	ip := "1.2.3.4"
	for i := 0; i < loginFreeFailures; i++ {
		l.fail(ip)
	}
	prev := time.Duration(0)
	for i := 0; i < 20; i++ {
		d := l.retryAfter(ip)
		if d <= 0 {
			t.Fatalf("round %d: want blocked", i)
		}
		if d < prev {
			t.Fatalf("round %d: backoff shrank from %v to %v", i, prev, d)
		}
		if d > loginBackoffMax {
			t.Fatalf("round %d: backoff %v exceeds cap %v", i, d, loginBackoffMax)
		}
		prev = d
		*now = now.Add(d)
		if l.retryAfter(ip) != 0 {
			t.Fatalf("round %d: want allowed once backoff elapsed", i)
		}
		l.fail(ip)
	}
	if prev != loginBackoffMax {
		t.Fatalf("want backoff to reach cap %v, got %v", loginBackoffMax, prev)
	}
}

func TestLoginLimiter_SuccessResets(t *testing.T) {
	l, _ := newTestLimiter(time.Unix(1000, 0))
	for i := 0; i < loginFreeFailures; i++ {
		l.fail("1.2.3.4")
	}
	l.reset("1.2.3.4")
	if d := l.retryAfter("1.2.3.4"); d != 0 {
		t.Fatalf("want allowed after reset, got retry after %v", d)
	}
}

func TestLoginLimiter_PruneDropsIdleEntries(t *testing.T) {
	l, now := newTestLimiter(time.Unix(1000, 0))
	l.fail("1.2.3.4")
	*now = now.Add(2*loginBackoffMax + time.Second)
	l.prune()
	l.mu.Lock()
	n := len(l.attempts)
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("want idle entry pruned, got %d entries", n)
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct{ remote, want string }{
		{"1.2.3.4:5555", "1.2.3.4"},
		{"[::1]:5555", "::1"},
		{"garbage", "garbage"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
		r.RemoteAddr = tt.remote
		if got := clientIP(r); got != tt.want {
			t.Errorf("clientIP(%q) = %q, want %q", tt.remote, got, tt.want)
		}
	}
}

func TestApiLoginHandler_RateLimitsBruteForce(t *testing.T) {
	old := loginLimit
	loginLimit = newLoginLimiter()
	t.Cleanup(func() { loginLimit = old })

	h := apiLoginHandler("admin", "secret", false)
	post := func(pw string) *httptest.ResponseRecorder {
		form := url.Values{"username": {"admin"}, "password": {pw}}
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = "9.9.9.9:1234"
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	for i := 0; i < loginFreeFailures; i++ {
		if w := post("wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i, w.Code)
		}
	}
	w := post("secret")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 while blocked even with correct password, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("want Retry-After header on 429")
	}
}
