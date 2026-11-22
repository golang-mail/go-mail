package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/golang-mail/go-mail/cache"
	"gopkg.in/yaml.v3"
	"os"
)

// Remote images in emails are blocked by the UI. This file stores, per
// account, the hosts the user chose to always load images from.
// Stored in image-hosts.yml as {account: [host, ...]}.

var imageHostsMu sync.Mutex

var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

func readImageHosts() (map[string][]string, error) {
	data, err := os.ReadFile(cache.ConfigPath("image-hosts.yml"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]string{}, nil
		}
		return nil, err
	}
	m := map[string][]string{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func writeImageHosts(m map[string][]string) error {
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return cache.WriteFileAtomic(cache.ConfigPath("image-hosts.yml"), data, 0o644)
}

// /api/image-hosts?account=X
//
//	GET            -> ["host", ...]
//	POST   {host}  -> add host (idempotent), returns the new list
//	DELETE ?host=  -> remove host, returns the new list
func imageHostsHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account := r.URL.Query().Get("account")
		store.Mu.RLock()
		known := findAccount(store, account) != nil
		store.Mu.RUnlock()
		if !known {
			http.Error(w, "unknown account", http.StatusNotFound)
			return
		}

		imageHostsMu.Lock()
		defer imageHostsMu.Unlock()
		all, err := readImageHosts()
		if err != nil {
			http.Error(w, "failed to read image hosts", http.StatusInternalServerError)
			return
		}

		switch r.Method {
		case http.MethodGet:
		case http.MethodPost, http.MethodDelete:
			var host string
			if r.Method == http.MethodPost {
				var body struct {
					Host string `json:"host"`
				}
				if err := decodeJSONBody(w, r, &body); err != nil {
					http.Error(w, "invalid JSON", http.StatusBadRequest)
					return
				}
				host = body.Host
			} else {
				host = r.URL.Query().Get("host")
			}
			host = strings.ToLower(strings.TrimSpace(host))
			if !hostnameRe.MatchString(host) {
				http.Error(w, "invalid host", http.StatusBadRequest)
				return
			}
			list := all[account]
			kept := list[:0:0]
			for _, h := range list {
				if h != host {
					kept = append(kept, h)
				}
			}
			if r.Method == http.MethodPost {
				kept = append(kept, host)
			}
			sort.Strings(kept)
			if len(kept) == 0 {
				delete(all, account)
			} else {
				all[account] = kept
			}
			if err := writeImageHosts(all); err != nil {
				http.Error(w, "failed to save image hosts", http.StatusInternalServerError)
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		out := all[account]
		if out == nil {
			out = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out) //nolint:errcheck
	}
}
