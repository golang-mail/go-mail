package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang-mail/go-mail/bayesian"
	"github.com/golang-mail/go-mail/cache"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var (
	sessions  = make(map[string]time.Time)
	mu        sync.Mutex
	filtersMu sync.Mutex
	labelsMu  sync.Mutex
	// Serialize read-modify-write cycles on the yml files across callers.
	filtersEditMu sync.Mutex
	labelsEditMu  sync.Mutex
)

// assetsHandler serves the operator's assets_dir (avatars, icons) to logged-in
// users only; the browser sends the session cookie with same-origin images.
func assetsHandler(prefix, dir string) http.Handler {
	files := http.StripPrefix(prefix, http.FileServer(http.Dir(dir)))
	return authMiddleware(files.ServeHTTP)
}

// staticHandler serves the embedded app assets to logged-in users only.
// login.html styles itself with remote fonts, so the login page needs
// nothing from here.
func staticHandler(staticFS fs.FS) http.HandlerFunc {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		slog.Error("static assets missing from embedded FS", "error", err)
		return http.NotFound
	}
	files := http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		files.ServeHTTP(w, r)
	}
}

// apiRoute wraps an authenticated API handler with request metrics and auth.
func apiRoute(pattern string, h http.HandlerFunc) http.HandlerFunc {
	return metricsMiddleware(pattern, authMiddleware(h))
}

func StartWebServer(store *cache.Store, runAllFiltersCh chan struct{}, runSelectedCh chan RunSelectedRequest, filtersRunning *atomic.Bool, filtersHaveRun *atomic.Bool, classifier *bayesian.Classifier, staticFS fs.FS, pluginMgr PluginManagerAPI) *http.Server {
	loadSessions()
	serverConfig, err := LoadServerConfig(cache.ConfigPath("server.yml"))
	if err != nil {
		slog.Error("failed to load server config", "error", err)
		os.Exit(1)
	}

	http.HandleFunc("/", homeHandler(staticFS))
	http.HandleFunc("/login", loginPageHandler(staticFS))
	http.HandleFunc("/metrics", apiRoute("/metrics", metricsHandler(serverConfig.Metrics, promhttp.Handler())))
	http.HandleFunc("/api/login", metricsMiddleware("/api/login", crossSiteMiddleware(apiLoginHandler(serverConfig.Username, serverConfig.Password, serverConfig.SecureCookies))))
	http.HandleFunc("/api/logout", apiRoute("/api/logout", logoutHandler(serverConfig.SecureCookies)))
	http.HandleFunc("/api/accounts", apiRoute("/api/accounts", accountsHandler(store)))
	http.HandleFunc("/api/unseen-summary", apiRoute("/api/unseen-summary", unseenSummaryHandler(store, filtersHaveRun)))
	http.HandleFunc("/api/mailboxes", apiRoute("/api/mailboxes", mailboxesHandler(store)))
	http.HandleFunc("/api/mailbox-settings", apiRoute("/api/mailbox-settings", mailboxSettingsHandler(serverConfig)))
	http.HandleFunc("/api/settings", apiRoute("/api/settings", clientSettingsHandler(serverConfig)))
	http.HandleFunc("/api/mailbox", apiRoute("/api/mailbox", mailboxHandler(store)))
	http.HandleFunc("/api/mails", apiRoute("/api/mails", mailsHandler(store)))
	http.HandleFunc("/api/mail", apiRoute("/api/mail", mailHandler(store)))
	http.HandleFunc("/api/mail/seen", apiRoute("/api/mail/seen", markAsSeenHandler(store)))
	http.HandleFunc("/api/mail/unseen", apiRoute("/api/mail/unseen", markAsUnseenHandler(store)))
	http.HandleFunc("/api/mail/delete", apiRoute("/api/mail/delete", deleteHandler(store)))
	http.HandleFunc("/api/mail/move", apiRoute("/api/mail/move", moveHandler(store)))
	http.HandleFunc("/api/mail/rsvp", apiRoute("/api/mail/rsvp", rsvpHandler(store)))
	http.HandleFunc("/api/mail/rsvp-links", apiRoute("/api/mail/rsvp-links", rsvpLinksHandler(store)))
	http.HandleFunc("/api/mail/send", apiRoute("/api/mail/send", sendMailHandler(store)))
	http.HandleFunc("/api/mail/draft", apiRoute("/api/mail/draft", saveDraftHandler(store)))
	http.HandleFunc("/api/mails/delete-selected", apiRoute("/api/mails/delete-selected", deleteAllHandler(store)))
	http.HandleFunc("/api/mails/move-selected", apiRoute("/api/mails/move-selected", moveSelectedHandler(store)))
	http.HandleFunc("/api/mails/mark-selected-as-read", apiRoute("/api/mails/mark-selected-as-read", markAllAsReadHandler(store)))
	http.HandleFunc("/api/search", apiRoute("/api/search", searchHandler(store)))
	http.HandleFunc("/api/attachment", apiRoute("/api/attachment", attachmentHandler(store)))
	http.HandleFunc("/api/events", apiRoute("/api/events", eventsHandler(Events)))
	http.HandleFunc("/api/sync-status", apiRoute("/api/sync-status", func(w http.ResponseWriter, r *http.Request) {
		status, _ := store.SyncStatus.Load().(string)
		if status == "" {
			status = "loading"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": status})
	}))
	http.HandleFunc("/api/filters/status", apiRoute("/api/filters/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"running": filtersRunning.Load(), "have_run": filtersHaveRun.Load()})
	}))
	http.HandleFunc("/api/filters/run-all", apiRoute("/api/filters/run-all", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		select {
		case runAllFiltersCh <- struct{}{}:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "triggered"})
		default:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "already_running"})
		}
	}))
	http.HandleFunc("/api/filters/run-selected", apiRoute("/api/filters/run-selected", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Account string   `json:"account"`
			Mailbox string   `json:"mailbox"`
			UIDs    []uint32 `json:"uids"`
			All     bool     `json:"all"`
		}
		if err := decodeJSONBody(w, r, &body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if body.Account == "" || body.Mailbox == "" || (len(body.UIDs) == 0 && !body.All) {
			http.Error(w, "account, mailbox, and uids are required", http.StatusBadRequest)
			return
		}
		if body.All {
			store.Mu.RLock()
			for _, acc := range store.Accounts {
				if acc.Name == body.Account {
					if mb, ok := acc.Mailboxes[body.Mailbox]; ok {
						body.UIDs = make([]uint32, 0, len(mb.Emails))
						for uid := range mb.Emails {
							body.UIDs = append(body.UIDs, uid)
						}
					}
					break
				}
			}
			store.Mu.RUnlock()
		}
		done := make(chan error, 1)
		select {
		case runSelectedCh <- RunSelectedRequest{
			Account: body.Account,
			Mailbox: body.Mailbox,
			UIDs:    body.UIDs,
			Done:    done,
		}:
		case <-r.Context().Done():
			return
		}
		select {
		case err := <-done:
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		case <-r.Context().Done():
			// Client walked away; the filter run continues in the background.
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "done"})
	}))
	http.HandleFunc("/api/filters", apiRoute("/api/filters", filtersHandler()))
	http.HandleFunc("/api/filters/", apiRoute("/api/filters/", filterItemHandler()))
	http.HandleFunc("/api/labels", apiRoute("/api/labels", labelsHandler()))
	http.HandleFunc("/api/labels/", apiRoute("/api/labels/", labelItemHandler()))
	http.HandleFunc("/api/mail/label", apiRoute("/api/mail/label", mailLabelHandler(store)))
	http.HandleFunc("/api/image-hosts", apiRoute("/api/image-hosts", imageHostsHandler(store)))
	http.HandleFunc("/api/contacts", apiRoute("/api/contacts", contactsHandler()))
	http.HandleFunc("/api/spam/train", apiRoute("/api/spam/train", spamTrainHandler(store, classifier)))
	http.HandleFunc("/api/spam/train-batch", apiRoute("/api/spam/train-batch", spamTrainBatchHandler(store, classifier)))
	http.HandleFunc("/api/spam/classify", apiRoute("/api/spam/classify", spamClassifyHandler(store, classifier)))
	http.HandleFunc("/api/spam/classify-batch", apiRoute("/api/spam/classify-batch", spamClassifyBatchHandler(store, classifier)))
	http.HandleFunc("/api/spam/stats", apiRoute("/api/spam/stats", spamStatsHandler(classifier)))
	http.HandleFunc("/api/spam/safelist", apiRoute("/api/spam/safelist", spamSafelistHandler(classifier)))
	http.HandleFunc("/api/plugins", apiRoute("/api/plugins", pluginsHandler(pluginMgr)))
	http.HandleFunc("/api/plugins/", apiRoute("/api/plugins/", pluginItemHandler(pluginMgr)))

	// Serve static assets (styles, images, etc.) from embedded FS
	http.Handle("/static/", staticHandler(staticFS))
	// Serve local assets (icons, avatars, etc.) from configured assets_dir
	if serverConfig.AssetsDir != "" {
		assetsPath := serverConfig.AssetsDir
		if !filepath.IsAbs(assetsPath) {
			assetsPath = cache.ConfigPath(assetsPath)
		}
		prefix := "/" + serverConfig.AssetsDir + "/"
		http.Handle(prefix, assetsHandler(prefix, assetsPath))
	}

	// Drop expired sessions on startup, then periodically.
	sweepExpiredSessions()
	go func() {
		for range time.Tick(time.Hour) {
			sweepExpiredSessions()
		}
	}()

	addr := fmt.Sprintf("%s:%d", serverConfig.Address, serverConfig.Port)
	slog.Info("starting web server", "address", addr)
	// Wrap the whole mux so security headers land on every response, including
	// static assets and the metrics endpoint.
	// No ReadTimeout/WriteTimeout: long multipart uploads and the new-mail
	// SSE stream must not be cut. ReadHeaderTimeout blocks Slowloris.
	srv := &http.Server{
		Addr:              addr,
		Handler:           securityHeaders(http.DefaultServeMux.ServeHTTP),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed to start", "error", err)
			os.Exit(1)
		}
	}()
	return srv
}

// noncePlaceholder in the HTML pages is swapped for the response's CSP nonce;
// versionPlaceholder for a per-process asset version so script URLs change on
// every restart and browsers never run a stale bundle against a new server.
const (
	noncePlaceholder   = "__CSP_NONCE__"
	versionPlaceholder = "__ASSET_VERSION__"
)

var assetVersion = strconv.FormatInt(time.Now().Unix(), 36)

func withNonce(page []byte, r *http.Request) []byte {
	page = bytes.ReplaceAll(page, []byte(noncePlaceholder), []byte(cspNonce(r)))
	return bytes.ReplaceAll(page, []byte(versionPlaceholder), []byte(assetVersion))
}

func homeHandler(staticFS fs.FS) http.HandlerFunc {
	data, _ := fs.ReadFile(staticFS, "static/index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(withNonce(data, r))
	}
}

func loginPageHandler(staticFS fs.FS) http.HandlerFunc {
	data, _ := fs.ReadFile(staticFS, "static/login.html")
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(withNonce(data, r))
	}
}
