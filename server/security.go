package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	netmail "net/mail"
	"net/url"
	"strings"
	"time"
)

// sanitizeRecipients parses each recipient and re-serializes it, rejecting any
// entry that fails to parse. This blocks header injection (CR/LF smuggling of
// extra headers like Bcc:) into the outgoing To:/Cc: header fields. Blank
// entries are skipped.
func sanitizeRecipients(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		addr, err := netmail.ParseAddress(s)
		if err != nil {
			return nil, err
		}
		out = append(out, addr.String())
	}
	return out, nil
}

// isForbiddenCrossSite reports whether a state-changing request looks like a
// cross-site (CSRF) request that must be rejected. Safe methods are never
// forbidden. It prefers the Sec-Fetch-Site hint sent by modern browsers and
// falls back to an Origin/Host comparison. Non-browser clients (curl, scripts)
// send neither header and are allowed, since they are not a CSRF vector.
func isForbiddenCrossSite(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site != "same-origin" && site != "none"
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return true
		}
		return u.Host != r.Host
	}
	return false
}

// smtpTLSConfig builds the TLS config for an outgoing SMTP connection.
// Verification is on by default; insecure is an explicit per-server opt-in for
// self-signed test servers.
func smtpTLSConfig(host string, insecure bool) *tls.Config {
	return &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: insecure, //nolint:gosec // opt-in per outgoing server for test setups
	}
}

type ctxKey int

const nonceKey ctxKey = iota

// cspNonce returns the script nonce securityHeaders issued for this request.
func cspNonce(r *http.Request) string {
	v, _ := r.Context().Value(nonceKey).(string)
	return v
}

func newNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Scripts run only from our origin or with the per-response nonce, which the
// page handlers stamp onto every <script> tag. Inline styles stay allowed.
const cspTemplate = "default-src 'self'; " +
	"script-src 'self' 'nonce-%s'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"font-src 'self'; " +
	"img-src 'self' data: https:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"frame-ancestors 'none'"

// securityHeaders sets baseline security response headers.
func securityHeaders(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nonce := newNonce()
		h := w.Header()
		h.Set("Content-Security-Policy", fmt.Sprintf(cspTemplate, nonce))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next(w, r.WithContext(context.WithValue(r.Context(), nonceKey, nonce)))
	}
}

// metricsHandler gates the Prometheus endpoint behind a config flag. When
// disabled it 404s so no mailbox/account names leak; when enabled it serves
// (callers wrap it in authMiddleware).
func metricsHandler(enabled bool, prom http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !enabled {
			http.NotFound(w, r)
			return
		}
		prom.ServeHTTP(w, r)
	}
}

// sweepExpiredSessions drops expired sessions and persists the result,
// returning how many were removed.
func sweepExpiredSessions() int {
	now := time.Now()
	mu.Lock()
	removed := 0
	for id, expiry := range sessions {
		if now.After(expiry) {
			delete(sessions, id)
			removed++
		}
	}
	mu.Unlock()
	if removed > 0 {
		saveSessions()
	}
	return removed
}
