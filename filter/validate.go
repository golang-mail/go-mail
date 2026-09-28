package filter

import (
	"fmt"
	"strings"
	"time"
)

var knownFields = map[string]bool{
	"from": true, "sender": true, "to": true, "cc": true, "bcc": true,
	"from/to": true, "from/to/cc/bcc": true,
	"subject": true, "body": true, "age": true, "status": true, "label": true,
}

var stringOperators = map[string]bool{
	"equals": true, "contains": true, "not-contains": true,
	"begins_with": true, "ends_with": true, "matches": true,
}

var knownActions = map[string]bool{
	"move": true, "delete": true, "permanent_delete": true,
	"mark_as_read": true, "mark_as_unread": true, "flag": true, "label": true,
	"mark_as_spam": true, "plugin_clear_cache": true,
}

// actionsNeedingValue must carry a target or name.
var actionsNeedingValue = map[string]bool{"move": true, "flag": true, "label": true}

// Validate rejects filters that would silently misbehave at run time:
// a misspelled field would otherwise never match, and an `and` filter
// made only of such conditions would match every email.
func Validate(f Filter) error {
	switch strings.ToLower(f.ConditionsType) {
	case "", "and", "or":
	default:
		return fmt.Errorf("filter %q: conditions_type must be and or or, got %q", f.Name, f.ConditionsType)
	}
	if len(f.Conditions) == 0 {
		return fmt.Errorf("filter %q: needs at least one condition", f.Name)
	}
	if len(f.Actions) == 0 {
		return fmt.Errorf("filter %q: needs at least one action", f.Name)
	}
	if err := validateSchedule(f, time.Now()); err != nil {
		return fmt.Errorf("filter %q: %w", f.Name, err)
	}
	for _, c := range f.Conditions {
		if err := validateCondition(c); err != nil {
			return fmt.Errorf("filter %q: %w", f.Name, err)
		}
	}
	for _, a := range f.Actions {
		t := strings.ToLower(a.Type)
		if !knownActions[t] {
			return fmt.Errorf("filter %q: unknown action %q", f.Name, a.Type)
		}
		if actionsNeedingValue[t] && strings.TrimSpace(a.Value) == "" {
			return fmt.Errorf("filter %q: action %q needs a value", f.Name, a.Type)
		}
	}
	return nil
}

func validateCondition(c Condition) error {
	field := strings.ToLower(c.Field)
	op := strings.ToLower(c.Operator)
	switch {
	case strings.HasPrefix(field, "header:"):
		if len(field) == len("header:") {
			return fmt.Errorf("header condition needs a header name")
		}
		if op != "exists" && !stringOperators[op] {
			return fmt.Errorf("unknown operator %q for %q", c.Operator, c.Field)
		}
	case strings.HasPrefix(field, "plugin:"):
		if len(field) == len("plugin:") {
			return fmt.Errorf("plugin condition needs a plugin name")
		}
	case field == "age":
		if op != "older_than" {
			return fmt.Errorf("age supports only older_than, got %q", c.Operator)
		}
		if _, err := ParseDuration(c.Value); err != nil {
			return fmt.Errorf("age %q: %w", c.Value, err)
		}
	case field == "status":
		v := strings.ToLower(c.Value)
		if v != "read" && v != "unread" {
			return fmt.Errorf("status must be read or unread, got %q", c.Value)
		}
	case knownFields[field]:
		if !stringOperators[op] {
			return fmt.Errorf("unknown operator %q for %q", c.Operator, c.Field)
		}
	default:
		return fmt.Errorf("unknown condition field %q", c.Field)
	}
	return nil
}
