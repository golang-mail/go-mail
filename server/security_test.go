package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSanitizeRecipients(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr bool
	}{
		{"plain email", []string{"a@b.com"}, []string{"<a@b.com>"}, false},
		{"name and email", []string{"Alice <a@b.com>"}, []string{`"Alice" <a@b.com>`}, false},
		{"blanks skipped", []string{"", "  ", "a@b.com"}, []string{"<a@b.com>"}, false},
		{"header injection via CRLF", []string{"a@b.com\r\nBcc: victim@x.com"}, nil, true},
		{"header injection via LF", []string{"a@b.com\nBcc: victim@x.com"}, nil, true},
		{"garbage", []string{"not-an-address"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sanitizeRecipients(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("sanitizeRecipients(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("sanitizeRecipients(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsForbiddenCrossSite(t *testing.T) {
	req := func(method, secFetch, origin, host string) *http.Request {
		r := httptest.NewRequest(method, "http://"+host+"/api/mail/delete", nil)
		r.Host = host
		if secFetch != "" {
			r.Header.Set("Sec-Fetch-Site", secFetch)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	tests := []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"GET is never forbidden", req("GET", "cross-site", "", "app:9090"), false},
		{"POST same-origin sec-fetch", req("POST", "same-origin", "", "app:9090"), false},
		{"POST none sec-fetch", req("POST", "none", "", "app:9090"), false},
		{"POST cross-site sec-fetch", req("POST", "cross-site", "", "app:9090"), true},
		{"POST same-site sec-fetch", req("POST", "same-site", "", "app:9090"), true},
		{"POST matching origin", req("POST", "", "http://app:9090", "app:9090"), false},
		{"POST mismatched origin", req("POST", "", "http://evil.com", "app:9090"), true},
		{"POST no browser headers", req("POST", "", "", "app:9090"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isForbiddenCrossSite(tt.r); got != tt.want {
				t.Errorf("isForbiddenCrossSite() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSMTPTLSConfig(t *testing.T) {
	secure := smtpTLSConfig("mail.example.com", false)
	if secure.InsecureSkipVerify {
		t.Error("default config must verify certificates")
	}
	if secure.ServerName != "mail.example.com" {
		t.Errorf("ServerName = %q, want mail.example.com", secure.ServerName)
	}
	insecure := smtpTLSConfig("mail.example.com", true)
	if !insecure.InsecureSkipVerify {
		t.Error("opt-in insecure config must skip verification")
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rw := httptest.NewRecorder()
	h(rw, httptest.NewRequest("GET", "/", nil))
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	}
	for k, v := range want {
		if got := rw.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	if csp := rw.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP missing default-src 'self': %q", csp)
	}
}

func TestMetricsHandlerDisabled(t *testing.T) {
	called := false
	prom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := metricsHandler(false, prom)
	rw := httptest.NewRecorder()
	h(rw, httptest.NewRequest("GET", "/metrics", nil))
	if rw.Code != http.StatusNotFound {
		t.Errorf("disabled metrics status = %d, want 404", rw.Code)
	}
	if called {
		t.Error("prometheus handler must not run when metrics disabled")
	}
}

func TestMetricsHandlerEnabled(t *testing.T) {
	called := false
	prom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := metricsHandler(true, prom)
	rw := httptest.NewRecorder()
	h(rw, httptest.NewRequest("GET", "/metrics", nil))
	if !called {
		t.Error("prometheus handler must run when metrics enabled")
	}
}

func TestSweepExpiredSessions(t *testing.T) {
	mu.Lock()
	sessions = map[string]time.Time{
		"live":    time.Now().Add(time.Hour),
		"expired": time.Now().Add(-time.Hour),
	}
	mu.Unlock()

	removed := sweepExpiredSessions()
	if removed != 1 {
		t.Errorf("sweepExpiredSessions removed %d, want 1", removed)
	}
	mu.Lock()
	_, liveOK := sessions["live"]
	_, expiredOK := sessions["expired"]
	mu.Unlock()
	if !liveOK {
		t.Error("live session was removed")
	}
	if expiredOK {
		t.Error("expired session was not removed")
	}
}

func TestLoginRejectsEmptyConfiguredCredentials(t *testing.T) {
	h := apiLoginHandler("", "", false)
	form := strings.NewReader("username=&password=")
	r := httptest.NewRequest("POST", "/api/login", form)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rw := httptest.NewRecorder()
	h(rw, r)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("empty-config login status = %d, want 401", rw.Code)
	}
}

func TestLoginSetsHardenedCookie(t *testing.T) {
	h := apiLoginHandler("admin", "secret", true)
	form := strings.NewReader("username=admin&password=secret")
	r := httptest.NewRequest("POST", "/api/login", form)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rw := httptest.NewRecorder()
	h(rw, r)

	cookies := rw.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == "mail_session_id" {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("no session cookie set (status %d)", rw.Code)
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if !session.Secure {
		t.Error("session cookie must be Secure when secureCookie=true")
	}
	if session.SameSite != http.SameSiteStrictMode {
		t.Error("session cookie must be SameSite=Strict")
	}
}

func TestLoginWrongPasswordRejected(t *testing.T) {
	h := apiLoginHandler("admin", "secret", false)
	form := strings.NewReader("username=admin&password=wrong")
	r := httptest.NewRequest("POST", "/api/login", form)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rw := httptest.NewRecorder()
	h(rw, r)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("wrong-password status = %d, want 401", rw.Code)
	}
}
