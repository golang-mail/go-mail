package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/golang-mail/go-mail/metrics"
)

func counterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	_ = c.Write(&m)
	return m.GetCounter().GetValue()
}

func TestLoginRejectsCrossSitePost(t *testing.T) {
	h := crossSiteMiddleware(apiLoginHandler("u", "p", false))
	form := strings.NewReader("username=u&password=p")
	req := httptest.NewRequest(http.MethodPost, "/api/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
}

// apiRoute must apply auth and count the request, whatever the handler.
func TestAPIRouteCountsAndAuthenticates(t *testing.T) {
	const pattern = "/api/test-route"
	h := apiRoute(pattern, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	counter := metrics.HTTPRequestsTotal.WithLabelValues(http.MethodGet, pattern, "401")
	before := counterValue(counter)

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, pattern, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: want 401 got %d", w.Code)
	}
	if got := counterValue(counter); got != before+1 {
		t.Fatalf("request counter: want %v got %v", before+1, got)
	}
}
