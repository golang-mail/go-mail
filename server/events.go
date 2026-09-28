package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// mailboxEvent tells the frontend that a mailbox changed. Empty account and
// mailbox mean "everything may have changed" (after a full filter run).
type mailboxEvent struct {
	Type    string `json:"type"`
	Account string `json:"account,omitempty"`
	Mailbox string `json:"mailbox,omitempty"`
}

// eventHub fans server-side changes out to connected SSE clients.
type eventHub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{subs: make(map[chan []byte]struct{})}
}

func (h *eventHub) subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *eventHub) subscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// publish never blocks: a subscriber that stopped reading just misses events,
// and the browser's periodic poll covers it.
func (h *eventHub) publish(ev mailboxEvent) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- raw:
		default:
		}
	}
}

// Events is the process-wide hub; main publishes, /api/events subscribes.
var Events = newEventHub()

// Subscribe returns a channel of raw event JSON and a function to stop.
func (h *eventHub) Subscribe() (<-chan []byte, func()) { return h.subscribe() }

// PublishMailboxChange notifies browsers that account/mailbox changed.
func PublishMailboxChange(account, mailbox string) {
	Events.publish(mailboxEvent{Type: "mailbox", Account: account, Mailbox: mailbox})
}

const eventsHeartbeat = 30 * time.Second

// eventsHandler streams hub events as server-sent events until the client goes away.
func eventsHandler(hub *eventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		ch, unsubscribe := hub.subscribe()
		defer unsubscribe()
		heartbeat := time.NewTicker(eventsHeartbeat)
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case raw := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", raw)
				flusher.Flush()
			case <-heartbeat.C:
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	}
}
