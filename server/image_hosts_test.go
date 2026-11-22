package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestImageHostsPerAccount(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })

	store := newHandlerStore(nil)
	store.Accounts = append(store.Accounts, &cache.Account{Name: "other"})
	h := imageHostsHandler(store)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(method, target, strings.NewReader(body)))
		return w
	}

	if w := do(http.MethodPost, "/api/image-hosts?account=acc", `{"host":"CDN.Example.com"}`); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `["cdn.example.com"]` {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	do(http.MethodPost, "/api/image-hosts?account=acc", `{"host":"cdn.example.com"}`) // idempotent
	if w := do(http.MethodGet, "/api/image-hosts?account=acc", ""); strings.TrimSpace(w.Body.String()) != `["cdn.example.com"]` {
		t.Fatalf("get: %s", w.Body)
	}
	if w := do(http.MethodGet, "/api/image-hosts?account=other", ""); strings.TrimSpace(w.Body.String()) != `[]` {
		t.Fatalf("other account must be empty: %s", w.Body)
	}
	for _, bad := range []string{`{"host":"a b"}`, `{"host":"x/y"}`, `{"host":""}`, `{"host":"-a.com"}`} {
		if w := do(http.MethodPost, "/api/image-hosts?account=acc", bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", bad, w.Code)
		}
	}
	if w := do(http.MethodGet, "/api/image-hosts?account=nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d", w.Code)
	}
	if w := do(http.MethodDelete, "/api/image-hosts?account=acc&host=cdn.example.com", ""); strings.TrimSpace(w.Body.String()) != `[]` {
		t.Fatalf("delete: %s", w.Body)
	}
	if w := do(http.MethodPut, "/api/image-hosts?account=acc", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("put: %d", w.Code)
	}
}
