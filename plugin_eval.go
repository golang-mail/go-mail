package main

import (
	"context"
	"strings"
	"time"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/filter"
	pluginpkg "github.com/golang-mail/go-mail/plugin"
)

// pluginEvaluatorBackend is the slice of *plugin.Manager the filter evaluator needs.
type pluginEvaluatorBackend interface {
	Get(name string) *pluginpkg.Client
	Evaluate(ctx context.Context, name string, req pluginpkg.EvaluateRequest) (*pluginpkg.EvaluateResponse, error)
}

// newPluginEvaluator wires plugin: conditions to the plugin manager.
//
// Runs from filter matching goroutines that hold no store lock, so every
// touch of email.PluginsRun goes through store.Mu.
func newPluginEvaluator(store *cache.Store, mgr pluginEvaluatorBackend) func(name string, email *cache.CachedEmail, filterConfig string, evalCtx filter.PluginEvalContext) (*filter.PluginVerdict, error) {
	return func(name string, email *cache.CachedEmail, filterConfig string, evalCtx filter.PluginEvalContext) (*filter.PluginVerdict, error) {
		// A recorded run means the side effect already fired; don't ask again.
		// plugin_clear_cache removes the mark, rerunnable plugins opt out.
		target := evalCtx.Target
		if target == nil {
			target = email
		}
		isRerunnable := false
		if c := mgr.Get(name); c != nil {
			isRerunnable = c.Manifest().IsRerunnable()
		}
		if !isRerunnable && pluginRunRecorded(store, target, name) {
			return &filter.PluginVerdict{Matched: false, Reason: "already ran"}, nil
		}
		req := pluginpkg.EvaluateRequest{
			Email: &pluginpkg.EvalEmail{
				UID:         email.UID,
				Account:     evalCtx.Account,
				Mailbox:     evalCtx.Mailbox,
				Envelope:    email.Envelope,
				Headers:     email.Headers,
				Body:        email.Body,
				ContentType: email.ContentType,
				Flags:       email.Flags,
				Labels:      email.Labels,
			},
			FilterConfig: filterConfig,
		}
		if email.Envelope != nil {
			req.Email.Date = email.Envelope.Date
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := mgr.Evaluate(ctx, name, req)
		if err != nil || resp == nil {
			return nil, err
		}
		// Record any successful response, matched or not: the plugin already decided.
		recordPluginRun(store, target, name)
		store.MarkDirty(evalCtx.Account, evalCtx.Mailbox)
		out := &filter.PluginVerdict{Matched: resp.Matched, Reason: resp.Reason}
		for _, a := range resp.Actions {
			out.Actions = append(out.Actions, filter.Action{Type: a.Type, Value: a.Value})
		}
		return out, nil
	}
}

func hasPluginRun(email *cache.CachedEmail, name string) bool {
	for _, p := range email.PluginsRun {
		if strings.EqualFold(p, name) {
			return true
		}
	}
	return false
}

func pluginRunRecorded(store *cache.Store, email *cache.CachedEmail, name string) bool {
	store.Mu.RLock()
	defer store.Mu.RUnlock()
	return hasPluginRun(email, name)
}

func recordPluginRun(store *cache.Store, email *cache.CachedEmail, name string) {
	store.Mu.Lock()
	defer store.Mu.Unlock()
	if !hasPluginRun(email, name) {
		email.PluginsRun = append(email.PluginsRun, name)
	}
}
