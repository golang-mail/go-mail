package server

import (
	"crypto/subtle"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/metrics"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func sessionsFilePath() string {
	return cache.ConfigPath("sessions.json")
}

func loadSessions() {
	mu.Lock()
	defer mu.Unlock()
	data, err := os.ReadFile(sessionsFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			sessions = make(map[string]time.Time)
		} else {
			slog.Error("failed to read sessions file", "error", err)
		}
		return
	}
	if err := json.Unmarshal(data, &sessions); err != nil {
		slog.Error("failed to unmarshal sessions", "error", err)
		sessions = make(map[string]time.Time)
	}
	// Older versions may have left the file readable by others.
	if err := os.Chmod(sessionsFilePath(), 0o600); err != nil {
		slog.Warn("failed to tighten sessions file permissions", "error", err)
	}
}

func saveSessions() {
	mu.Lock()
	defer mu.Unlock()
	metrics.ActiveSessions.Set(float64(len(sessions)))
	data, err := json.Marshal(sessions)
	if err != nil {
		slog.Error("failed to marshal sessions", "error", err)
		return
	}
	if err := writeFilePrivateAtomic(sessionsFilePath(), data); err != nil {
		slog.Error("failed to write sessions file", "error", err)
	}
}

// writeFilePrivateAtomic writes via a temp file and rename so a crash never
// truncates the target, and forces 0600 even on a pre-existing file.
func writeFilePrivateAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Chmod(path, 0o600)
}

func apiLoginHandler(username, password string, secureCookie bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		ip := clientIP(r)
		if wait := loginLimit.retryAfter(ip); wait > 0 {
			metrics.LoginAttempts.WithLabelValues("throttled").Inc()
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			http.Error(w, "Too many failed logins, try again later", http.StatusTooManyRequests)
			return
		}
		userOK := subtle.ConstantTimeCompare([]byte(r.FormValue("username")), []byte(username)) == 1
		passOK := passwordMatches(password, r.FormValue("password"))
		// Reject unconfigured credentials so a server.yml missing username/password
		// can never be satisfied by an empty (or any) submission.
		if username == "" || !userOK || !passOK {
			metrics.LoginAttempts.WithLabelValues("failure").Inc()
			loginLimit.fail(ip)
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}
		loginLimit.reset(ip)
		metrics.LoginAttempts.WithLabelValues("success").Inc()
		sessionID := uuid.New().String()
		mu.Lock()
		sessions[sessionID] = time.Now().Add(24 * time.Hour)
		mu.Unlock()
		saveSessions()
		http.SetCookie(w, &http.Cookie{
			Name:     "mail_session_id",
			Value:    sessionID,
			Expires:  time.Now().Add(24 * time.Hour),
			Path:     "/",
			HttpOnly: true,
			Secure:   secureCookie,
			SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

func logoutHandler(secureCookie bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cookie, err := r.Cookie("mail_session_id"); err == nil {
			mu.Lock()
			delete(sessions, cookie.Value)
			mu.Unlock()
			saveSessions()
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "mail_session_id",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   secureCookie,
			SameSite: http.SameSiteStrictMode,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "logged_out"})
	}
}

// crossSiteMiddleware rejects unsafe-method requests from another origin.
func crossSiteMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isForbiddenCrossSite(r) {
			http.Error(w, "cross-site request forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	guarded := crossSiteMiddleware(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoggedIn(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		guarded(w, r)
	}
}

func isLoggedIn(r *http.Request) bool {
	cookie, err := r.Cookie("mail_session_id")
	if err != nil {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	expiry, ok := sessions[cookie.Value]
	if !ok || time.Now().After(expiry) {
		delete(sessions, cookie.Value)
		return false
	}
	return true
}
