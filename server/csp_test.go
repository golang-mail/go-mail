package server

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var nonceRE = regexp.MustCompile(`'nonce-([A-Za-z0-9+/=_-]+)'`)

func TestSecurityHeaders_ScriptNonceReplacesUnsafeInline(t *testing.T) {
	var seen string
	h := securityHeaders(func(w http.ResponseWriter, r *http.Request) {
		seen = cspNonce(r)
		w.WriteHeader(http.StatusOK)
	})
	rw := httptest.NewRecorder()
	h(rw, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rw.Header().Get("Content-Security-Policy")

	script := ""
	for _, d := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(d), "script-src") {
			script = d
		}
	}
	if strings.Contains(script, "'unsafe-inline'") {
		t.Fatalf("script-src still allows unsafe-inline: %q", script)
	}
	m := nonceRE.FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("script-src has no nonce: %q", script)
	}
	if seen == "" || seen != m[1] {
		t.Fatalf("handler saw nonce %q, header has %q", seen, m[1])
	}

	rw2 := httptest.NewRecorder()
	h(rw2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rw2.Header().Get("Content-Security-Policy") == csp {
		t.Fatal("nonce must differ per request")
	}
}

func TestPageHandlers_InjectNonceIntoScriptTags(t *testing.T) {
	mu.Lock()
	sessions["tok"] = time.Now().Add(time.Hour)
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); delete(sessions, "tok"); mu.Unlock() })

	pages := fstest.MapFS{
		"static/index.html": {Data: []byte(`<script nonce="__CSP_NONCE__">a</script><script nonce="__CSP_NONCE__" src="/static/x.js"></script>`)},
		"static/login.html": {Data: []byte(`<script nonce="__CSP_NONCE__">b</script>`)},
	}
	for name, h := range map[string]http.HandlerFunc{"home": homeHandler(pages), "login": loginPageHandler(pages)} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(&http.Cookie{Name: "mail_session_id", Value: "tok"})
			rw := httptest.NewRecorder()
			securityHeaders(h)(rw, req)
			if rw.Code != http.StatusOK {
				t.Fatalf("status %d", rw.Code)
			}
			m := nonceRE.FindStringSubmatch(rw.Header().Get("Content-Security-Policy"))
			if m == nil {
				t.Fatal("no nonce in CSP")
			}
			body := rw.Body.String()
			if strings.Contains(body, "__CSP_NONCE__") {
				t.Fatalf("placeholder left in page: %s", body)
			}
			if strings.Count(body, `nonce="`+m[1]+`"`) != strings.Count(body, "<script") {
				t.Fatalf("every script tag must carry the response nonce: %s", body)
			}
		})
	}
}

// Script URLs carry a version so browsers refetch after an upgrade.
func TestPageHandlers_StampAssetVersion(t *testing.T) {
	mu.Lock()
	sessions["tok2"] = time.Now().Add(time.Hour)
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); delete(sessions, "tok2"); mu.Unlock() })
	pages := fstest.MapFS{"static/index.html": {Data: []byte(`<script nonce="__CSP_NONCE__" src="/static/app/core.js?v=__ASSET_VERSION__"></script>`)}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "mail_session_id", Value: "tok2"})
	rw := httptest.NewRecorder()
	securityHeaders(homeHandler(pages))(rw, req)
	body := rw.Body.String()
	if strings.Contains(body, "__ASSET_VERSION__") || !strings.Contains(body, "?v="+assetVersion) || assetVersion == "" {
		t.Fatalf("asset version not stamped: %s (version %q)", body, assetVersion)
	}
}
