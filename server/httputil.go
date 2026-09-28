package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/golang-mail/go-mail/metrics"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Snippet-extraction regexes used by unseenSummaryHandler. Hoisted so each
// /api/unseen-summary request doesn't recompile them per email.
var (
	snippetStyleRE  = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	snippetScriptRE = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	snippetTagRE    = regexp.MustCompile(`<[^>]*>`)
	snippetEntityRE = regexp.MustCompile(`&#?\w+;`)
)

var snippetEntityReplacer = strings.NewReplacer(
	"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
	"&quot;", "\"", "&#39;", "'", "&apos;", "'",
)

// extractSnippet produces a short plain-text preview from an email body.
// Truncates the input to a reasonable size first so HTML stripping doesn't
// pay megabyte costs for a 150-char output.
func extractSnippet(body string) string {
	if body == "" {
		return ""
	}
	// Bound the work: 8KB of HTML is more than enough to find 150 chars of
	// visible text. Larger bodies fall back to this prefix.
	const snippetBudget = 8 * 1024
	if len(body) > snippetBudget {
		body = body[:snippetBudget]
	}
	s := snippetStyleRE.ReplaceAllString(body, "")
	s = snippetScriptRE.ReplaceAllString(s, "")
	s = snippetTagRE.ReplaceAllString(s, " ")
	s = snippetEntityReplacer.Replace(s)
	s = snippetEntityRE.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 150 {
		s = s[:150] + "…"
	}
	return s
}

// maxJSONBody caps API request bodies; attachments travel as multipart.
const maxJSONBody = 4 << 20

// decodeJSONBody reads a JSON request body with a size cap.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	return json.NewDecoder(r.Body).Decode(v)
}

// writeJSONWithETag marshals data to JSON, computes an ETag, and returns 304
// if the client already has the current version (via If-None-Match header).
func writeJSONWithETag(w http.ResponseWriter, r *http.Request, data any) {
	buf, err := json.Marshal(data)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
	hash := sha256.Sum256(buf)
	etag := fmt.Sprintf(`"%x"`, hash[:8])

	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")

	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(buf)
}

func metricsMiddleware(pattern string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rw, r)
		metrics.HTTPRequestDuration.WithLabelValues(r.Method, pattern).Observe(time.Since(start).Seconds())
		metrics.HTTPRequestsTotal.WithLabelValues(r.Method, pattern, strconv.Itoa(rw.status)).Inc()
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush keeps streaming handlers (server-sent events) working behind the wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
