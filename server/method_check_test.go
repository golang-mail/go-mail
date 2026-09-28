package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every state-changing handler must refuse GET, otherwise the cross-site
// check (which skips safe methods) never applies to it.
func TestMutatingHandlersRejectGET(t *testing.T) {
	store := newHandlerStore(nil)
	handlers := map[string]http.HandlerFunc{
		"/api/mail/seen":                   markAsSeenHandler(store),
		"/api/mail/unseen":                 markAsUnseenHandler(store),
		"/api/mail/delete":                 deleteHandler(store),
		"/api/mail/move":                   moveHandler(store),
		"/api/mails/delete-selected":       deleteAllHandler(store),
		"/api/mails/move-selected":         moveSelectedHandler(store),
		"/api/mails/mark-selected-as-read": markAllAsReadHandler(store),
		"/api/logout":                      logoutHandler(false),
	}
	for path, h := range handlers {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h(w, httptest.NewRequest(http.MethodGet, path+"?account=acc&mailbox=INBOX&uid=1&from=INBOX&to=X", nil))
			if w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("GET %s: want 405, got %d", path, w.Code)
			}
		})
	}
}
