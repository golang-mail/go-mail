package filter

import (
	"fmt"
	"strings"
	"time"
)

const (
	OnExpireDisable = "disable"
	OnExpireRemove  = "remove"
)

// ActiveAt reports whether the filter should run at now. Unparseable
// timestamps fail closed so a hand-edited typo never widens a filter's window.
func (f Filter) ActiveAt(now time.Time) bool {
	if f.Disabled {
		return false
	}
	if f.ActiveFrom != "" {
		from, err := time.Parse(time.RFC3339, f.ActiveFrom)
		if err != nil || now.Before(from) {
			return false
		}
	}
	if f.ActiveUntil != "" {
		until, err := time.Parse(time.RFC3339, f.ActiveUntil)
		if err != nil || !now.Before(until) {
			return false
		}
	}
	return true
}

// Active returns a copy holding only the filters that should run at now.
func (c Config) Active(now time.Time) Config {
	out := Config{Filters: make([]Filter, 0, len(c.Filters))}
	for _, f := range c.Filters {
		if f.ActiveAt(now) {
			out.Filters = append(out.Filters, f)
		}
	}
	return out
}

// Sweep materializes schedule boundaries that have passed: a reached
// active_from is cleared, a reached active_until disables or removes the
// filter. Returns one description per change; nil when nothing was due.
func (c *Config) Sweep(now time.Time) []string {
	var changes []string
	kept := make([]Filter, 0, len(c.Filters))
	for _, f := range c.Filters {
		if f.ActiveUntil != "" {
			until, err := time.Parse(time.RFC3339, f.ActiveUntil)
			if err == nil && !now.Before(until) {
				if strings.EqualFold(f.OnExpire, OnExpireRemove) {
					changes = append(changes, fmt.Sprintf("removed %q", f.Name))
					continue
				}
				f.Disabled = true
				f.ActiveFrom, f.ActiveUntil, f.OnExpire = "", "", ""
				changes = append(changes, fmt.Sprintf("disabled %q", f.Name))
				kept = append(kept, f)
				continue
			}
		}
		if f.ActiveFrom != "" {
			from, err := time.Parse(time.RFC3339, f.ActiveFrom)
			if err == nil && !now.Before(from) {
				f.ActiveFrom = ""
				changes = append(changes, fmt.Sprintf("activated %q", f.Name))
			}
		}
		kept = append(kept, f)
	}
	c.Filters = kept
	return changes
}

// validateSchedule checks the timestamps and on_expire so a filter cannot be
// saved with a window that never opens or a past end.
func validateSchedule(f Filter, now time.Time) error {
	var from, until time.Time
	var err error
	if f.ActiveFrom != "" {
		if from, err = time.Parse(time.RFC3339, f.ActiveFrom); err != nil {
			return fmt.Errorf("active_from %q: %w", f.ActiveFrom, err)
		}
	}
	if f.ActiveUntil != "" {
		if until, err = time.Parse(time.RFC3339, f.ActiveUntil); err != nil {
			return fmt.Errorf("active_until %q: %w", f.ActiveUntil, err)
		}
		if !until.After(now) {
			return fmt.Errorf("active_until %q is in the past", f.ActiveUntil)
		}
		if f.ActiveFrom != "" && !until.After(from) {
			return fmt.Errorf("active_until %q is not after active_from %q", f.ActiveUntil, f.ActiveFrom)
		}
	}
	switch strings.ToLower(f.OnExpire) {
	case "", OnExpireDisable, OnExpireRemove:
	default:
		return fmt.Errorf("on_expire must be disable or remove, got %q", f.OnExpire)
	}
	return nil
}
