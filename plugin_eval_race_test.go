package main

import (
	"context"
	"testing"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/filter"
	pluginpkg "github.com/golang-mail/go-mail/plugin"
)

type fakePluginBackend struct{}

func (fakePluginBackend) Get(string) *pluginpkg.Client { return nil }

func (fakePluginBackend) Evaluate(context.Context, string, pluginpkg.EvaluateRequest) (*pluginpkg.EvaluateResponse, error) {
	return &pluginpkg.EvaluateResponse{Matched: true}, nil
}

// Handlers read PluginsRun under the store read lock; the evaluator runs in
// filter goroutines and must record runs under the write lock.
func TestPluginEvaluatorRecordsRunUnderStoreLock(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })

	emails := map[uint32]*cache.CachedEmail{}
	for uid := uint32(1); uid <= 50; uid++ {
		emails[uid] = testEmail("plugin me", false)
	}
	store, account := newFilterTestStore(t, emails)

	oldEval := filter.PluginEvaluator
	filter.PluginEvaluator = newPluginEvaluator(store, &fakePluginBackend{})
	t.Cleanup(func() { filter.PluginEvaluator = oldEval })

	cfg := filter.Config{Filters: []filter.Filter{{
		ID:         "plg",
		Name:       "Plugin filter",
		Mailboxes:  []string{"INBOX"},
		Conditions: []filter.Condition{{Field: "plugin:fake", Operator: "equals", Value: "x"}},
		Actions:    []filter.Action{{Type: "label", Value: "seen-by-plugin"}},
	}}}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			store.Mu.RLock()
			for _, e := range emails {
				_ = len(e.PluginsRun)
			}
			store.Mu.RUnlock()
		}
	}()

	filterAccount(store, account, cfg, true, false, "", nil, false)
	close(stop)
	<-done

	for uid, e := range emails {
		if len(e.PluginsRun) != 1 || e.PluginsRun[0] != "fake" {
			t.Fatalf("uid %d: want PluginsRun [fake], got %v", uid, e.PluginsRun)
		}
	}
}
