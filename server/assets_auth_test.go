package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func TestAssetsHandler_RequiresLogin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "avatar.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := assetsHandler("/assets/", dir)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/avatar.png", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous asset fetch: want 401 got %d", w.Code)
	}

	mu.Lock()
	sessions["asset-tok"] = time.Now().Add(time.Hour)
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); delete(sessions, "asset-tok"); mu.Unlock() })
	req := httptest.NewRequest(http.MethodGet, "/assets/avatar.png", nil)
	req.AddCookie(&http.Cookie{Name: "mail_session_id", Value: "asset-tok"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "png" {
		t.Fatalf("logged-in asset fetch: %d %q", w.Code, w.Body.String())
	}
}

// The embedded app assets (including the raw index.html template) must not be
// reachable without a session; only the self-hosted fonts are public.
func TestStaticHandler_RequiresLogin(t *testing.T) {
	fakeFS := fstest.MapFS{
		"static/index.html":      &fstest.MapFile{Data: []byte("<html>__CSP_NONCE__</html>")},
		"static/styles.css":      &fstest.MapFile{Data: []byte("body{}")},
		"static/app/init.js":     &fstest.MapFile{Data: []byte("console.log(1)")},
		"static/fonts/fonts.css": &fstest.MapFile{Data: []byte("@font-face{}")},
	}
	h := staticHandler(fakeFS)

	for _, path := range []string{"/static/index.html", "/static/styles.css", "/static/app/init.js"} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
			t.Fatalf("anonymous %s: want redirect to /login, got %d %q", path, w.Code, w.Header().Get("Location"))
		}
	}

	// Fonts are public (the login page uses them); path tricks must not widen that.
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/static/fonts/fonts.css", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("anonymous font css: want 200, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/static/fonts/../index.html", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("anonymous traversal out of fonts: want redirect, got %d", w.Code)
	}

	mu.Lock()
	sessions["static-tok"] = time.Now().Add(time.Hour)
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); delete(sessions, "static-tok"); mu.Unlock() })
	req := httptest.NewRequest(http.MethodGet, "/static/styles.css", nil)
	req.AddCookie(&http.Cookie{Name: "mail_session_id", Value: "static-tok"})
	w = httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "body{}" {
		t.Fatalf("logged-in asset fetch: %d %q", w.Code, w.Body)
	}
}
