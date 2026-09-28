package server

import (
	"encoding/json"
	"github.com/golang-mail/go-mail/cache"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
	"sort"
	"strings"
)

func mailboxSettingsHandler(serverConfig *ServerConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(serverConfig.MailboxSettings)
	}
}

func clientSettingsHandler(serverConfig *ServerConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		spamMbox := serverConfig.SpamMailbox
		if spamMbox == "" {
			spamMbox = "Junk"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"new_mail_sound": serverConfig.NewMailSound,
			"spam_mailbox":   spamMbox,
		})
	}
}

// /api/contacts -> GET all unique email addresses
func contactsHandler() http.HandlerFunc {
	type contactEntry struct {
		Email string `yaml:"email"`
	}
	type contactsFile struct {
		Groups map[string]map[string][]contactEntry `yaml:"groups"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := os.ReadFile(cache.ConfigPath("contacts.yaml"))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]")) //nolint:errcheck
			return
		}
		var cf contactsFile
		if err := yaml.Unmarshal(data, &cf); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]")) //nolint:errcheck
			return
		}
		seen := make(map[string]bool)
		var emails []string
		for _, companies := range cf.Groups {
			for _, contacts := range companies {
				for _, c := range contacts {
					lower := strings.ToLower(c.Email)
					if lower != "" && !seen[lower] {
						seen[lower] = true
						emails = append(emails, c.Email)
					}
				}
			}
		}
		sort.Strings(emails)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(emails) //nolint:errcheck
	}
}
