package server

import (
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/filter"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
	"strings"
	"time"
)

// helpers to read/write filters.yml
func readFiltersFile() (filter.Config, error) {
	filtersMu.Lock()
	defer filtersMu.Unlock()
	var cfg filter.Config
	data, err := os.ReadFile(cache.ConfigPath("filters.yml"))
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ensureFilterIDs assigns ULID IDs to filters that lack them and sets sensible defaults
func ensureFilterIDs(cfg *filter.Config) {
	changed := false
	for i := range cfg.Filters {
		f := &cfg.Filters[i]
		if f.ID == "" {
			// simple ULID-like id: timestamp in ms + random hex
			b := make([]byte, 10)
			if _, err := crand.Read(b); err != nil {
				// fallback to uuid
				f.ID = uuid.New().String()
			} else {
				f.ID = fmt.Sprintf("%d%x", time.Now().UnixMilli(), b)
			}
			changed = true
		}
		if f.ConditionsType == "" {
			f.ConditionsType = "and"
			changed = true
		}
		if f.Name == "" {
			f.Name = "(unnamed)"
			changed = true
		}
	}
	if changed {
		// best-effort write back
		_ = writeFiltersFile(*cfg)
	}
}

func writeFiltersFile(cfg filter.Config) error {
	filtersMu.Lock()
	defer filtersMu.Unlock()
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return cache.WriteFileAtomic(cache.ConfigPath("filters.yml"), data, 0o644)
}

// /api/filters  -> GET list, POST create
func filtersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			cfg, err := readFiltersFile()
			if err != nil {
				http.Error(w, "failed to read filters", http.StatusInternalServerError)
				return
			}
			// ensure filters have stable IDs and defaults
			ensureFilterIDs(&cfg)
			// Return flattened objects expected by the UI. For backward compatibility
			// keep the original filter under "filter" as well.
			// Always an array: the UI calls .filter on it.
			out := make([]map[string]interface{}, 0, len(cfg.Filters))
			for _, f := range cfg.Filters {
				// Determine single account/mailbox if provided (UI expects strings)
				acct := ""
				mbox := ""
				if len(f.Accounts) > 0 {
					acct = f.Accounts[0]
				}
				if len(f.Mailboxes) > 0 {
					mbox = f.Mailboxes[0]
				}

				// Try to extract a simple condition/action if present (best-effort)
				field := ""
				op := ""
				val := ""
				if len(f.Conditions) > 0 {
					field = f.Conditions[0].Field
					op = f.Conditions[0].Operator
					val = f.Conditions[0].Value
				}

				action := ""
				actionParam := ""
				if len(f.Actions) > 0 {
					action = f.Actions[0].Type
					actionParam = f.Actions[0].Value
				}

				m := map[string]interface{}{
					"id":           f.ID,
					"name":         f.Name,
					"account":      acct,
					"mailbox":      mbox,
					"field":        field,
					"op":           op,
					"value":        val,
					"action":       action,
					"action_param": actionParam,
					"filter":       f,
				}
				out = append(out, m)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)
			return
		case http.MethodPost:
			filtersEditMu.Lock()
			defer filtersEditMu.Unlock()
			// Accept either the full filter.Filter JSON or a flat payload coming from the UI.
			var nf filter.Filter
			// First try to decode into a map to support flat shape
			var bodyMap map[string]interface{}
			if err := decodeJSONBody(w, r, &bodyMap); err != nil {
				http.Error(w, "invalid payload", http.StatusBadRequest)
				return
			}

			// If caller provided nested filter object, prefer that
			if v, ok := bodyMap["filter"]; ok {
				// marshal-unmarshal into Filter
				buf, _ := json.Marshal(v)
				if err := json.Unmarshal(buf, &nf); err != nil {
					http.Error(w, "invalid nested filter", http.StatusBadRequest)
					return
				}
			} else if _, ok := bodyMap["field"]; ok {
				// flat UI payload
				acct := []string{}
				if s, ok := bodyMap["account"].(string); ok && s != "" {
					acct = append(acct, s)
				}
				mbox := []string{}
				if s, ok := bodyMap["mailbox"].(string); ok && s != "" {
					mbox = append(mbox, s)
				}
				conds := []filter.Condition{}
				if f, ok := bodyMap["field"].(string); ok && f != "" {
					op := "contains"
					if s, ok := bodyMap["op"].(string); ok && s != "" {
						op = s
					}
					val := ""
					if s, ok := bodyMap["value"].(string); ok {
						val = s
					}
					conds = append(conds, filter.Condition{Field: f, Operator: op, Value: val})
				}
				acts := []filter.Action{}
				if a, ok := bodyMap["action"].(string); ok && a != "" {
					v := ""
					if s, ok := bodyMap["action_param"].(string); ok {
						v = s
					}
					acts = append(acts, filter.Action{Type: a, Value: v})
				}
				nf = filter.Filter{
					Name:       "",
					Accounts:   acct,
					Mailboxes:  mbox,
					Conditions: conds,
					Actions:    acts,
				}
			} else {
				// Try to decode directly into filter.Filter (legacy)
				buf, _ := json.Marshal(bodyMap)
				if err := json.Unmarshal(buf, &nf); err != nil {
					http.Error(w, "invalid payload shape", http.StatusBadRequest)
					return
				}
				// Accept snake_case key from the UI
				if v, ok := bodyMap["conditions_type"].(string); ok {
					nf.ConditionsType = v
				}
			}

			if err := filter.Validate(nf); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			cfg, err := readFiltersFile()
			if err != nil {
				// if file missing, start new
				cfg = filter.Config{}
			}
			// ensure id, name and conditions_type
			if nf.ID == "" {
				b := make([]byte, 10)
				if _, err := crand.Read(b); err != nil {
					nf.ID = uuid.New().String()
				} else {
					nf.ID = fmt.Sprintf("%d%x", time.Now().UnixMilli(), b)
				}
			}
			if nf.ConditionsType == "" {
				nf.ConditionsType = "and"
			}
			if nf.Name == "" {
				nf.Name = "(unnamed)"
			}
			cfg.Filters = append(cfg.Filters, nf)
			// persist any generated IDs/defaults
			ensureFilterIDs(&cfg)
			if err := writeFiltersFile(cfg); err != nil {
				http.Error(w, "failed to save", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}
}

// /api/filters/{id} -> GET, PUT, DELETE
func filterItemHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filtersEditMu.Lock()
		defer filtersEditMu.Unlock()
		// id is the path after /api/filters/
		id := strings.TrimPrefix(r.URL.Path, "/api/filters/")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		cfg, err := readFiltersFile()
		if err != nil {
			http.Error(w, "failed to read filters", http.StatusInternalServerError)
			return
		}
		ensureFilterIDs(&cfg)

		// find by ID
		idx := -1
		for i := range cfg.Filters {
			if cfg.Filters[i].ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		switch r.Method {
		case http.MethodGet:
			// Return flattened object similar to list endpoint so the UI can read simple fields
			f := cfg.Filters[idx]
			acct := ""
			mbox := ""
			if len(f.Accounts) > 0 {
				acct = f.Accounts[0]
			}
			if len(f.Mailboxes) > 0 {
				mbox = f.Mailboxes[0]
			}
			field := ""
			op := ""
			val := ""
			if len(f.Conditions) > 0 {
				field = f.Conditions[0].Field
				op = f.Conditions[0].Operator
				val = f.Conditions[0].Value
			}
			action := ""
			actionParam := ""
			if len(f.Actions) > 0 {
				action = f.Actions[0].Type
				actionParam = f.Actions[0].Value
			}
			out := map[string]interface{}{
				"id":           id,
				"name":         f.Name,
				"account":      acct,
				"mailbox":      mbox,
				"field":        field,
				"op":           op,
				"value":        val,
				"action":       action,
				"action_param": actionParam,
				"filter":       f,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)
			return
		case http.MethodPut:
			// Accept either nested filter or flat UI payload. Decode into a map first.
			var bodyMap map[string]interface{}
			if err := decodeJSONBody(w, r, &bodyMap); err != nil {
				http.Error(w, "invalid payload", http.StatusBadRequest)
				return
			}

			var updated filter.Filter
			if v, ok := bodyMap["filter"]; ok {
				buf, _ := json.Marshal(v)
				if err := json.Unmarshal(buf, &updated); err != nil {
					http.Error(w, "invalid nested filter", http.StatusBadRequest)
					return
				}
			} else if _, ok := bodyMap["field"]; ok {
				acct := []string{}
				if s, ok := bodyMap["account"].(string); ok && s != "" {
					acct = append(acct, s)
				}
				mbox := []string{}
				if s, ok := bodyMap["mailbox"].(string); ok && s != "" {
					mbox = append(mbox, s)
				}
				conds := []filter.Condition{}
				if f, ok := bodyMap["field"].(string); ok && f != "" {
					op := "contains"
					if s, ok := bodyMap["op"].(string); ok && s != "" {
						op = s
					}
					val := ""
					if s, ok := bodyMap["value"].(string); ok {
						val = s
					}
					conds = append(conds, filter.Condition{Field: f, Operator: op, Value: val})
				}
				acts := []filter.Action{}
				if a, ok := bodyMap["action"].(string); ok && a != "" {
					v := ""
					if s, ok := bodyMap["action_param"].(string); ok {
						v = s
					}
					acts = append(acts, filter.Action{Type: a, Value: v})
				}
				updated = filter.Filter{
					Name:       "",
					Accounts:   acct,
					Mailboxes:  mbox,
					Conditions: conds,
					Actions:    acts,
				}
			} else {
				buf, _ := json.Marshal(bodyMap)
				if err := json.Unmarshal(buf, &updated); err != nil {
					http.Error(w, "invalid payload shape", http.StatusBadRequest)
					return
				}
				if v, ok := bodyMap["conditions_type"].(string); ok {
					updated.ConditionsType = v
				}
			}

			if err := filter.Validate(updated); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			cfg.Filters[idx] = updated
			if err := writeFiltersFile(cfg); err != nil {
				http.Error(w, "failed to save", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		case http.MethodDelete:
			// remove the filter at idx
			cfg.Filters = append(cfg.Filters[:idx], cfg.Filters[idx+1:]...)
			if err := writeFiltersFile(cfg); err != nil {
				http.Error(w, "failed to save", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}
}

// SweepFilterSchedules applies filter schedule boundaries that have passed
// and rewrites filters.yml when anything changed. Runs under filtersEditMu
// so it never interleaves with an API edit.
func SweepFilterSchedules(now time.Time) ([]string, error) {
	filtersEditMu.Lock()
	defer filtersEditMu.Unlock()
	cfg, err := readFiltersFile()
	if err != nil {
		return nil, err
	}
	changes := cfg.Sweep(now)
	if len(changes) == 0 {
		return nil, nil
	}
	if err := writeFiltersFile(cfg); err != nil {
		return nil, err
	}
	return changes, nil
}
