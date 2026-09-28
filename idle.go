package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/emersion/go-imap/client"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/metrics"
)

const (
	idleDebounce   = 2 * time.Second
	idleBackoffMin = 5 * time.Second
	idleBackoffMax = 5 * time.Minute
	idleLogoutEach = 25 * time.Minute // servers may drop IDLE after 30 minutes
)

// idleEnabled reports whether the account should keep a push connection.
// On by default; `idle: false` opts out, send-only accounts never idle.
func idleEnabled(acc *cache.Account) bool {
	if acc.SendOnly {
		return false
	}
	return acc.Idle == nil || *acc.Idle
}

// idleConns counts live IDLE sessions per account, for tests and metrics.
var idleConns struct {
	mu sync.Mutex
	n  map[string]int
}

func idleConnections(account string) int {
	idleConns.mu.Lock()
	defer idleConns.mu.Unlock()
	return idleConns.n[account]
}

func idleConnChange(account string, delta int) {
	idleConns.mu.Lock()
	if idleConns.n == nil {
		idleConns.n = map[string]int{}
	}
	idleConns.n[account] += delta
	n := idleConns.n[account]
	idleConns.mu.Unlock()
	metrics.IdleConnections.WithLabelValues(account).Set(float64(n))
}

// debounce calls fire once per burst of signals: d after the last signal.
func debounce(ctx context.Context, in <-chan struct{}, d time.Duration, fire func()) {
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-in:
			if timer == nil {
				timer = time.NewTimer(d)
			} else {
				timer.Reset(d)
			}
			timerC = timer.C
		case <-timerC:
			timerC = nil
			fire()
		}
	}
}

// runIdleLoop keeps one IMAP connection idling on mailbox and calls onChange
// (debounced) whenever the server reports new, changed or expunged messages.
// It returns when ctx ends or when the server turns out not to support IDLE.
func runIdleLoop(ctx context.Context, acc *cache.Account, mailbox string, debounceFor time.Duration, onChange func(mailbox string)) {
	backoff := idleBackoffMin
	for ctx.Err() == nil {
		supported, err := idleSession(ctx, acc, mailbox, debounceFor, onChange)
		if !supported {
			slog.Info("server does not support IDLE, relying on periodic sync", "account", acc.Name)
			return
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("IDLE session ended, reconnecting", "account", acc.Name, "mailbox", mailbox, "error", err, "retry_in", backoff)
			metrics.IMAPErrors.WithLabelValues(acc.Name, "idle").Inc()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, idleBackoffMax)
	}
}

// idleSession runs a single connection until it fails or ctx ends.
// supported=false means the server lacks IDLE and the caller should give up.
func idleSession(ctx context.Context, acc *cache.Account, mailbox string, debounceFor time.Duration, onChange func(string)) (supported bool, err error) {
	c, err := acc.Connect()
	if err != nil {
		return true, err
	}
	defer c.Logout()

	ok, err := c.Support("IDLE")
	if err != nil {
		return true, err
	}
	if !ok {
		return false, nil
	}
	if _, err := c.Select(mailbox, true); err != nil {
		return true, err
	}

	updates := make(chan client.Update, 64)
	c.Updates = updates
	trigger := make(chan struct{}, 16)
	dctx, dcancel := context.WithCancel(ctx)
	defer dcancel()
	go debounce(dctx, trigger, debounceFor, func() {
		metrics.IdleEvents.WithLabelValues(acc.Name).Inc()
		onChange(mailbox)
	})

	stop := make(chan struct{})
	idleErr := make(chan error, 1)
	go func() {
		idleErr <- c.Idle(stop, &client.IdleOptions{LogoutTimeout: idleLogoutEach, PollInterval: -1})
	}()
	idleConnChange(acc.Name, 1)
	defer idleConnChange(acc.Name, -1)
	slog.Info("IDLE started", "account", acc.Name, "mailbox", mailbox)

	for {
		select {
		case <-ctx.Done():
			close(stop)
			<-idleErr
			return true, nil
		case err := <-idleErr:
			return true, err
		case u := <-updates:
			switch u.(type) {
			case *client.MailboxUpdate, *client.MessageUpdate, *client.ExpungeUpdate:
				select {
				case trigger <- struct{}{}:
				default:
				}
			}
		}
	}
}

// idleMailboxes returns the mailboxes to watch: idle_mailboxes, else INBOX.
func idleMailboxes(acc *cache.Account) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range acc.IdleMailboxes {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	if len(out) == 0 {
		return []string{"INBOX"}
	}
	return out
}

// startIdleLoops launches one IDLE loop per watched mailbox of each eligible account.
func startIdleLoops(ctx context.Context, store *cache.Store, onChange func(acc *cache.Account, mailbox string)) {
	store.Mu.RLock()
	accounts := append([]*cache.Account(nil), store.Accounts...)
	store.Mu.RUnlock()
	for _, acc := range accounts {
		if !idleEnabled(acc) {
			continue
		}
		for _, mailbox := range idleMailboxes(acc) {
			go runIdleLoop(ctx, acc, mailbox, idleDebounce, func(mailbox string) { onChange(acc, mailbox) })
		}
	}
}
