package filter

import (
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestUnknownConditionFieldNeverMatches(t *testing.T) {
	email := &cache.CachedEmail{Envelope: &cache.Envelope{Subject: "hello"}, Body: "hello"}
	for _, ct := range []string{"and", "or"} {
		f := Filter{
			Name:           "typo",
			ConditionsType: ct,
			Conditions:     []Condition{{Field: "subjekt", Operator: "contains", Value: "hello"}},
		}
		if matched, _ := MatchesCachedWithActions(email, f, PluginEvalContext{}); matched {
			t.Errorf("%s filter with only an unknown field must not match", ct)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Filter{
		Name:       "ok",
		Conditions: []Condition{{Field: "subject", Operator: "contains", Value: "x"}, {Field: "header:X-Spam", Operator: "exists"}, {Field: "plugin:foo", Operator: "equals", Value: "y"}, {Field: "age", Operator: "older_than", Value: "30d"}},
		Actions:    []Action{{Type: "move", Value: "Archive"}, {Type: "mark_as_read"}},
	}
	tests := []struct {
		name    string
		mutate  func(f *Filter)
		wantErr bool
	}{
		{"valid", func(*Filter) {}, false},
		{"unknown field", func(f *Filter) { f.Conditions[0].Field = "subjekt" }, true},
		{"unknown operator", func(f *Filter) { f.Conditions[0].Operator = "startswith" }, true},
		{"exists on non-header", func(f *Filter) { f.Conditions[0].Operator = "exists" }, true},
		{"unknown action", func(f *Filter) { f.Actions[0].Type = "moove" }, true},
		{"move without target", func(f *Filter) { f.Actions[0].Value = "" }, true},
		{"bad conditions type", func(f *Filter) { f.ConditionsType = "xor" }, true},
		{"bad age duration", func(f *Filter) { f.Conditions[3].Value = "soon" }, true},
		{"no conditions", func(f *Filter) { f.Conditions = nil }, true},
		{"no actions", func(f *Filter) { f.Actions = nil }, true},
		{"case-insensitive names", func(f *Filter) { f.Conditions[0].Field = "Subject"; f.Actions[1].Type = "Mark_As_Read" }, false},
		{"disabled", func(f *Filter) { f.Disabled = true }, false},
		{"future schedule", func(f *Filter) {
			f.ActiveFrom = "2099-01-01T00:00:00Z"
			f.ActiveUntil = "2099-06-01T00:00:00Z"
			f.OnExpire = "remove"
		}, false},
		{"active_from in the past is fine", func(f *Filter) { f.ActiveFrom = "2000-01-01T00:00:00Z" }, false},
		{"bad active_from", func(f *Filter) { f.ActiveFrom = "tomorrow" }, true},
		{"bad active_until", func(f *Filter) { f.ActiveUntil = "2099-13-01" }, true},
		{"active_until in the past", func(f *Filter) { f.ActiveUntil = "2000-01-01T00:00:00Z" }, true},
		{"active_until before active_from", func(f *Filter) { f.ActiveFrom = "2099-06-01T00:00:00Z"; f.ActiveUntil = "2099-01-01T00:00:00Z" }, true},
		{"bad on_expire", func(f *Filter) { f.OnExpire = "archive" }, true},
		{"on_expire without active_until", func(f *Filter) { f.OnExpire = "remove" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := ok
			f.Conditions = append([]Condition(nil), ok.Conditions...)
			f.Actions = append([]Action(nil), ok.Actions...)
			tt.mutate(&f)
			err := Validate(f)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
