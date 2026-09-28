package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

// The UI calls .filter on the response, so an empty list must be [] not null.
func TestFiltersHandler_EmptyListIsJSONArray(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })
	if err := os.WriteFile(cache.ConfigPath("filters.yml"), []byte("filters: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	filtersHandler()(w, httptest.NewRequest(http.MethodGet, "/api/filters", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Fatalf("empty filters list = %q, want []", body)
	}
}
