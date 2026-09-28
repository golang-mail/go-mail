package filter

import (
	"reflect"
	"testing"
	"time"
)

var (
	schedNow    = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	schedPast   = schedNow.Add(-time.Hour).Format(time.RFC3339)
	schedFuture = schedNow.Add(time.Hour).Format(time.RFC3339)
)

func TestFilterActiveAt(t *testing.T) {
	tests := []struct {
		name string
		f    Filter
		want bool
	}{
		{"plain", Filter{}, true},
		{"disabled", Filter{Disabled: true}, false},
		{"disabled wins over schedule", Filter{Disabled: true, ActiveFrom: schedPast}, false},
		{"before active_from", Filter{ActiveFrom: schedFuture}, false},
		{"after active_from", Filter{ActiveFrom: schedPast}, true},
		{"before active_until", Filter{ActiveUntil: schedFuture}, true},
		{"after active_until", Filter{ActiveUntil: schedPast}, false},
		{"inside window", Filter{ActiveFrom: schedPast, ActiveUntil: schedFuture}, true},
		{"unparseable timestamp fails closed", Filter{ActiveFrom: "tomorrow"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.ActiveAt(schedNow); got != tt.want {
				t.Fatalf("ActiveAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigActive(t *testing.T) {
	cfg := Config{Filters: []Filter{
		{Name: "on"},
		{Name: "off", Disabled: true},
		{Name: "later", ActiveFrom: schedFuture},
	}}
	got := cfg.Active(schedNow)
	if len(got.Filters) != 1 || got.Filters[0].Name != "on" {
		t.Fatalf("Active() = %+v, want only \"on\"", got.Filters)
	}
	if len(cfg.Filters) != 3 {
		t.Fatal("Active() must not mutate the receiver")
	}
}

func TestConfigSweep(t *testing.T) {
	tests := []struct {
		name        string
		in          []Filter
		want        []Filter
		wantChanges int
	}{
		{"nothing due", []Filter{{Name: "a"}, {Name: "b", ActiveFrom: schedFuture, ActiveUntil: schedFuture}}, []Filter{{Name: "a"}, {Name: "b", ActiveFrom: schedFuture, ActiveUntil: schedFuture}}, 0},
		{"active_from reached is cleared", []Filter{{Name: "a", ActiveFrom: schedPast}}, []Filter{{Name: "a"}}, 1},
		{"active_until reached disables by default", []Filter{{Name: "a", ActiveFrom: schedPast, ActiveUntil: schedPast}}, []Filter{{Name: "a", Disabled: true}}, 1},
		{"active_until reached with on_expire disable", []Filter{{Name: "a", ActiveUntil: schedPast, OnExpire: "disable"}}, []Filter{{Name: "a", Disabled: true}}, 1},
		{"active_until reached with on_expire remove", []Filter{{Name: "a"}, {Name: "gone", ActiveUntil: schedPast, OnExpire: "remove"}, {Name: "c"}}, []Filter{{Name: "a"}, {Name: "c"}}, 1},
		{"unparseable timestamps left alone", []Filter{{Name: "a", ActiveUntil: "soon"}}, []Filter{{Name: "a", ActiveUntil: "soon"}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Filters: append([]Filter(nil), tt.in...)}
			changes := cfg.Sweep(schedNow)
			if len(changes) != tt.wantChanges {
				t.Fatalf("Sweep() changes = %v, want %d", changes, tt.wantChanges)
			}
			if !reflect.DeepEqual(cfg.Filters, tt.want) {
				t.Fatalf("Sweep() left %+v, want %+v", cfg.Filters, tt.want)
			}
		})
	}
}
