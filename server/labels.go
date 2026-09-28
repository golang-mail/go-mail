package server

import (
	"encoding/json"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/golang-mail/go-mail/cache"
	"gopkg.in/yaml.v3"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// Label types and helpers for labels.yml
type Label struct {
	Name     string   `yaml:"name" json:"name"`
	Color    string   `yaml:"color" json:"color"`
	Accounts []string `yaml:"accounts,omitempty" json:"accounts,omitempty"`
	// Storage controls where the label is stored when assigned to an email.
	// "local" (default, empty) puts it in CachedEmail.Labels — go-mail's own
	// cache, invisible to other mail clients. "imap" stores it as a custom
	// IMAP keyword in CachedEmail.Flags and pushes to the IMAP server, so
	// Thunderbird/Apple Mail/etc. see it as a server-side tag.
	Storage string `yaml:"storage,omitempty" json:"storage,omitempty"`
	// Hidden suppresses per-email chip rendering. The label still applies and
	// can be matched by filters; it just isn't shown in mail list rows or the
	// email detail view. Useful for internal flags like "slack" plugin markers.
	Hidden bool `yaml:"hidden,omitempty" json:"hidden,omitempty"`
	// Weight controls chip display order. Lower values render first; negatives
	// move a label to the front, positives push it to the back. Ties broken by
	// name. Default 0.
	Weight int `yaml:"weight,omitempty" json:"weight,omitempty"`
}

// LabelStorageIMAP reports whether the label is configured as an IMAP keyword.
func (l Label) LabelStorageIMAP() bool { return strings.EqualFold(l.Storage, "imap") }

type LabelsConfig struct {
	Labels []Label `yaml:"labels" json:"labels"`
}

// ReadLabels is the exported accessor for labels.yml. Used by callers outside
// the server package (e.g. main.go wiring host.list_labels for plugins) to
// avoid a server→plugin import cycle.
func ReadLabels() (LabelsConfig, error) { return readLabelsFile() }

func readLabelsFile() (LabelsConfig, error) {
	labelsMu.Lock()
	defer labelsMu.Unlock()
	var cfg LabelsConfig
	data, err := os.ReadFile(cache.ConfigPath("labels.yml"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func writeLabelsFile(cfg LabelsConfig) error {
	labelsMu.Lock()
	defer labelsMu.Unlock()
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return cache.WriteFileAtomic(cache.ConfigPath("labels.yml"), data, 0o644)
}

// autoLabelColors are assigned to labels auto-registered by filter/plugin
// actions. The slot is picked deterministically from the label name so a given
// label always gets the same color even before the operator customizes it.
var autoLabelColors = []string{
	"#1a73e8", "#34a853", "#fbbc04", "#ea4335", "#9334e6",
	"#00897b", "#f06292", "#ff7043", "#7cb342", "#5e35b1",
}

func autoLabelColor(name string) string {
	var h uint32
	for i := 0; i < len(name); i++ {
		h = h*131 + uint32(name[i])
	}
	return autoLabelColors[int(h)%len(autoLabelColors)]
}

// EnsureLabel registers a label definition if no entry with the same name
// already exists, so labels added by filter/plugin actions show up in the UI's
// Labels tab and can be customized. `storage` should be "local" or "imap".
// Existing definitions are never overwritten.
func EnsureLabel(name, storage string) error {
	if name == "" {
		return nil
	}
	labelsEditMu.Lock()
	defer labelsEditMu.Unlock()
	cfg, err := readLabelsFile()
	if err != nil {
		return err
	}
	for _, l := range cfg.Labels {
		if strings.EqualFold(l.Name, name) {
			return nil
		}
	}
	cfg.Labels = append(cfg.Labels, Label{
		Name:    name,
		Color:   autoLabelColor(name),
		Storage: storage,
	})
	return writeLabelsFile(cfg)
}

// /api/labels  -> GET list, POST create
func labelsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			cfg, err := readLabelsFile()
			if err != nil {
				http.Error(w, "failed to read labels", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(cfg.Labels)
		case http.MethodPost:
			labelsEditMu.Lock()
			defer labelsEditMu.Unlock()
			var label Label
			if err := decodeJSONBody(w, r, &label); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if label.Name == "" {
				http.Error(w, "name is required", http.StatusBadRequest)
				return
			}
			cfg, err := readLabelsFile()
			if err != nil {
				http.Error(w, "failed to read labels", http.StatusInternalServerError)
				return
			}
			for _, existing := range cfg.Labels {
				if strings.EqualFold(existing.Name, label.Name) {
					http.Error(w, "label already exists", http.StatusConflict)
					return
				}
			}
			cfg.Labels = append(cfg.Labels, label)
			if err := writeLabelsFile(cfg); err != nil {
				http.Error(w, "failed to save", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(label)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// /api/labels/{name}  -> GET, PUT, DELETE
func labelItemHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		labelsEditMu.Lock()
		defer labelsEditMu.Unlock()
		name := strings.TrimPrefix(r.URL.Path, "/api/labels/")
		if name == "" {
			http.Error(w, "label name required", http.StatusBadRequest)
			return
		}

		cfg, err := readLabelsFile()
		if err != nil {
			http.Error(w, "failed to read labels", http.StatusInternalServerError)
			return
		}

		idx := -1
		for i, l := range cfg.Labels {
			if strings.EqualFold(l.Name, name) {
				idx = i
				break
			}
		}

		switch r.Method {
		case http.MethodGet:
			if idx == -1 {
				http.Error(w, "label not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(cfg.Labels[idx])
		case http.MethodPut:
			var label Label
			if err := decodeJSONBody(w, r, &label); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if label.Name == "" {
				label.Name = name
			}
			if idx == -1 {
				cfg.Labels = append(cfg.Labels, label)
			} else {
				cfg.Labels[idx] = label
			}
			if err := writeLabelsFile(cfg); err != nil {
				http.Error(w, "failed to save", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(label)
		case http.MethodDelete:
			if idx == -1 {
				http.Error(w, "label not found", http.StatusNotFound)
				return
			}
			cfg.Labels = append(cfg.Labels[:idx], cfg.Labels[idx+1:]...)
			if err := writeLabelsFile(cfg); err != nil {
				http.Error(w, "failed to save", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// /api/mail/label  -> POST add/remove label on an email
func mailLabelHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Account string `json:"account"`
			Mailbox string `json:"mailbox"`
			UID     uint32 `json:"uid"`
			Label   string `json:"label"`
			Action  string `json:"action"` // "add" or "remove"
		}
		if err := decodeJSONBody(w, r, &req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Label == "" {
			http.Error(w, "label is required", http.StatusBadRequest)
			return
		}

		// For ADD: route by labels.yml definition (default local if undefined).
		// For REMOVE: always strip from BOTH msg.Labels AND msg.Flags so we don't
		// silently leave a keyword behind because labels.yml is stale or missing.
		labelsCfg, err := readLabelsFile()
		if err != nil {
			// Fall back to local storage, but say why routing may be wrong.
			slog.Warn("failed to read labels.yml, defaulting to local label storage", "error", err)
		}
		useIMAP := false
		for _, l := range labelsCfg.Labels {
			if strings.EqualFold(l.Name, req.Label) {
				useIMAP = l.LabelStorageIMAP()
				break
			}
		}

		store.Mu.Lock()
		var resultLabels, resultFlags []string
		var foundAcc *cache.Account
		var removedFromFlags bool
		var found bool
		for i := range store.Accounts {
			acc := store.Accounts[i]
			if acc.Name != req.Account {
				continue
			}
			mbox, ok := acc.Mailboxes[req.Mailbox]
			if !ok {
				store.Mu.Unlock()
				http.Error(w, "mailbox not found", http.StatusNotFound)
				return
			}
			msg, ok := mbox.Emails[req.UID]
			if !ok {
				store.Mu.Unlock()
				http.Error(w, "email not found", http.StatusNotFound)
				return
			}
			if req.Action == "remove" {
				// Strip from msg.Labels.
				filteredLabels := make([]string, 0, len(msg.Labels))
				for _, l := range msg.Labels {
					if !strings.EqualFold(l, req.Label) {
						filteredLabels = append(filteredLabels, l)
					}
				}
				msg.Labels = filteredLabels
				// Strip from msg.Flags and remember so we fire IMAP RemoveFlags.
				filteredFlags := make([]string, 0, len(msg.Flags))
				for _, f := range msg.Flags {
					if strings.EqualFold(f, req.Label) {
						removedFromFlags = true
						continue
					}
					filteredFlags = append(filteredFlags, f)
				}
				msg.Flags = filteredFlags
			} else {
				target := &msg.Labels
				if useIMAP {
					target = &msg.Flags
				}
				has := false
				for _, l := range *target {
					if strings.EqualFold(l, req.Label) {
						has = true
						break
					}
				}
				if !has {
					*target = append(*target, req.Label)
				}
			}
			resultLabels = append([]string(nil), msg.Labels...)
			resultFlags = append([]string(nil), msg.Flags...)
			foundAcc = acc
			found = true
			// One row changed; a whole-mailbox mark would re-hash every email.
			store.MarkEmailDirty(acc.Name, req.Mailbox, req.UID)
			break //nolint:staticcheck // single-account iteration, break is intentional
		}
		store.Mu.Unlock()

		if !found {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
		store.SaveCache()

		// Push the keyword change to IMAP when relevant:
		//   - add: only if the label definition is IMAP-storage.
		//   - remove: whenever the keyword was actually in msg.Flags, regardless of
		//     whether labels.yml is up to date — handles orphaned plugin flags whose
		//     definition is missing or has drifted.
		pushIMAP := false
		if req.Action == "remove" && removedFromFlags {
			pushIMAP = true
		} else if req.Action != "remove" && useIMAP {
			pushIMAP = true
		}
		if pushIMAP && foundAcc != nil {
			go withMailbox(foundAcc, req.Mailbox, "push label keyword to IMAP", func(c *client.Client) error {
				var op imap.FlagsOp = imap.AddFlags
				if req.Action == "remove" {
					op = imap.RemoveFlags
				}
				return storeFlags(c, []uint32{req.UID}, op, req.Label)
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"labels": resultLabels,
			"flags":  resultFlags,
			"imap":   useIMAP,
		})
	}
}
