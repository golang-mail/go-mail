package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

// The editor sends snake_case keys; they must land on the filter and round-trip.
func TestFiltersHandler_ScheduleFieldsRoundTrip(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })
	if err := os.WriteFile(cache.ConfigPath("filters.yml"), []byte("filters: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"paused","disabled":true,"active_until":"2099-01-01T00:00:00Z","on_expire":"remove","conditions":[{"field":"subject","operator":"contains","value":"x"}],"actions":[{"type":"mark_as_read"}]}`
	w := httptest.NewRecorder()
	filtersHandler()(w, httptest.NewRequest(http.MethodPost, "/api/filters", strings.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201 got %d: %s", w.Code, w.Body.String())
	}
	cfg, err := readFiltersFile()
	if err != nil {
		t.Fatal(err)
	}
	f := cfg.Filters[0]
	if !f.Disabled || f.ActiveUntil != "2099-01-01T00:00:00Z" || f.OnExpire != "remove" {
		t.Fatalf("schedule fields lost on save: %+v", f)
	}

	w = httptest.NewRecorder()
	filtersHandler()(w, httptest.NewRequest(http.MethodGet, "/api/filters", nil))
	for _, key := range []string{`"disabled":true`, `"active_until":"2099-01-01T00:00:00Z"`, `"on_expire":"remove"`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Fatalf("GET response lacks %s: %s", key, w.Body.String())
		}
	}

	w = httptest.NewRecorder()
	filtersHandler()(w, httptest.NewRequest(http.MethodPost, "/api/filters", strings.NewReader(strings.Replace(body, "2099", "2000", 1))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("past active_until should be rejected, got %d", w.Code)
	}
}
