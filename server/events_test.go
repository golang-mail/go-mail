package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEventHub_PublishReachesEverySubscriber(t *testing.T) {
	hub := newEventHub()
	a, unsubA := hub.subscribe()
	defer unsubA()
	b, unsubB := hub.subscribe()
	defer unsubB()

	hub.publish(mailboxEvent{Type: "mailbox", Account: "acc", Mailbox: "INBOX"})

	for name, ch := range map[string]<-chan []byte{"a": a, "b": b} {
		select {
		case raw := <-ch:
			var ev mailboxEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatalf("%s: bad payload %q: %v", name, raw, err)
			}
			if ev.Type != "mailbox" || ev.Account != "acc" || ev.Mailbox != "INBOX" {
				t.Fatalf("%s: got %+v", name, ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s: no event", name)
		}
	}
}

func TestEventHub_SlowSubscriberDoesNotBlockPublish(t *testing.T) {
	hub := newEventHub()
	_, unsub := hub.subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			hub.publish(mailboxEvent{Type: "mailbox"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a subscriber that never reads")
	}
}

func TestEventsHandler_StreamsServerSentEvents(t *testing.T) {
	hub := newEventHub()
	srv := httptest.NewServer(eventsHandler(hub))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}

	// Give the handler a moment to register before publishing.
	deadline := time.Now().Add(2 * time.Second)
	for hub.subscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	hub.publish(mailboxEvent{Type: "mailbox", Account: "acc", Mailbox: "INBOX"})

	r := bufio.NewReader(resp.Body)
	var data string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			break
		}
	}
	var ev mailboxEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil || ev.Account != "acc" {
		t.Fatalf("data line %q (%v)", data, err)
	}
}

// The metrics wrapper must not hide the Flusher the SSE handler needs.
func TestEventsHandler_StreamsThroughMetricsMiddleware(t *testing.T) {
	hub := newEventHub()
	srv := httptest.NewServer(metricsMiddleware("/api/events", eventsHandler(hub)))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d content-type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
