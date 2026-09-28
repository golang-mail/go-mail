package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestFiltersHandler_RejectsUnknownField(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })
	if err := os.WriteFile(cache.ConfigPath("filters.yml"), []byte("filters: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	body := `{"filter":{"name":"typo","conditions":[{"field":"subjekt","operator":"contains","value":"x"}],"actions":[{"type":"mark_as_read"}]}}`
	w := httptest.NewRecorder()
	filtersHandler()(w, httptest.NewRequest(http.MethodPost, "/api/filters", strings.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "subjekt") {
		t.Fatalf("error should name the bad field: %s", w.Body.String())
	}
}
