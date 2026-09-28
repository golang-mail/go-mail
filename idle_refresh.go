package main

import (
	"log/slog"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/server"
)

// idleRefresh is what an IDLE notice triggers: sync that one mailbox, filter
// the account's new mail, persist, and tell connected browsers.
func idleRefresh(store *cache.Store, acc *cache.Account, mailbox string) {
	c, err := acc.Acquire()
	if err != nil {
		slog.Error("idle refresh: connect failed", "account", acc.Name, "error", err)
		return
	}
	syncMailbox(store, acc, c, mailbox)
	acc.Release(c)

	cfg, err := loadFiltersConfig()
	if err != nil {
		slog.Error("idle refresh: filters config", "error", err)
	} else {
		filterAccount(store, acc, cfg, false, false, "", nil, filterDebug)
	}
	store.SaveCache()
	updateCacheMetrics(store)
	server.PublishMailboxChange(acc.Name, mailbox)
}
