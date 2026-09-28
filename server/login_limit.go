package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	// loginFreeFailures is how many failures an IP gets before backoff starts.
	loginFreeFailures = 5
	loginBackoffBase  = 5 * time.Second
	loginBackoffMax   = 15 * time.Minute
	// loginPruneAfter drops an IP's record once it has been idle this long.
	loginPruneAfter = 2 * loginBackoffMax
)

type loginAttempt struct {
	failures     int
	blockedUntil time.Time
	lastSeen     time.Time
}

// loginLimiter applies per-IP exponential backoff to failed logins.
type loginLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	attempts map[string]*loginAttempt
}

var loginLimit = newLoginLimiter()

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{now: time.Now, attempts: make(map[string]*loginAttempt)}
}

// retryAfter returns how long ip must wait, or 0 if it may attempt now.
func (l *loginLimiter) retryAfter(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[ip]
	if !ok {
		return 0
	}
	if d := a.blockedUntil.Sub(l.now()); d > 0 {
		return d
	}
	return 0
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a, ok := l.attempts[ip]
	if !ok {
		a = &loginAttempt{}
		l.attempts[ip] = a
	}
	a.failures++
	a.lastSeen = now
	if a.failures >= loginFreeFailures {
		a.blockedUntil = now.Add(loginBackoff(a.failures - loginFreeFailures))
	}
	if len(l.attempts) > 1024 {
		l.pruneLocked(now)
	}
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.attempts, ip)
	l.mu.Unlock()
}

func (l *loginLimiter) prune() {
	l.mu.Lock()
	l.pruneLocked(l.now())
	l.mu.Unlock()
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	for ip, a := range l.attempts {
		if now.Sub(a.lastSeen) > loginPruneAfter {
			delete(l.attempts, ip)
		}
	}
}

// loginBackoff doubles per excess failure, capped at loginBackoffMax.
func loginBackoff(excess int) time.Duration {
	d := loginBackoffBase
	for i := 0; i < excess && d < loginBackoffMax; i++ {
		d *= 2
	}
	if d > loginBackoffMax {
		d = loginBackoffMax
	}
	return d
}

// clientIP uses the TCP peer only; forwarded headers are spoofable.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
