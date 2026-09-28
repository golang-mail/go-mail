package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestAllMetricsRegisteredUnderGomailPrefix(t *testing.T) {
	// Touch one child of each vector so the gatherer has something to report.
	FilterRunsTotal.WithLabelValues("test").Inc()
	HTTPRequestsTotal.WithLabelValues("GET", "/x", "200").Inc()
	EmailsSent.WithLabelValues("me@x", "success").Inc()
	CachedEmails.WithLabelValues("acc", "INBOX").Set(1)
	AccountsTotal.Set(1)
	ActiveSessions.Set(1)
	HTTPRequestDuration.WithLabelValues("GET", "/x").Observe(0.1)

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"gomail_filter_runs_total":             false,
		"gomail_http_requests_total":           false,
		"gomail_emails_sent_total":             false,
		"gomail_cached_emails":                 false,
		"gomail_accounts_total":                false,
		"gomail_active_sessions":               false,
		"gomail_http_request_duration_seconds": false,
	}
	for _, f := range families {
		if _, ok := want[f.GetName()]; ok {
			want[f.GetName()] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("metric %s not registered", name)
		}
	}
}

func TestCounterIncrements(t *testing.T) {
	c := HTTPRequestsTotal.WithLabelValues("POST", "/api/test", "201")
	read := func() float64 {
		var m dto.Metric
		_ = c.Write(&m)
		return m.GetCounter().GetValue()
	}
	before := read()
	c.Inc()
	if got := read(); got != before+1 {
		t.Fatalf("counter %v -> %v, want +1", before, got)
	}
}
