package main

import (
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchDebounce coalesces the burst of events one editor save produces.
const watchDebounce = 500 * time.Millisecond

// watchConfigFiles watches the config directory, not the two files: editors
// and sed -i save via rename, which would orphan a per-file inotify watch.
func watchConfigFiles(dir string, onFilters, onPlugins func()) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, err
	}
	go func() {
		var mu sync.Mutex
		timers := map[string]*time.Timer{}
		schedule := func(name string, fn func()) {
			mu.Lock()
			defer mu.Unlock()
			if t := timers[name]; t != nil {
				t.Stop()
			}
			timers[name] = time.AfterFunc(watchDebounce, fn)
		}
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
					continue
				}
				switch filepath.Base(event.Name) {
				case "filters.yml":
					schedule("filters", func() {
						slog.Info("filters file modified, running all filters", "file", event.Name)
						onFilters()
					})
				case "plugins.yml":
					schedule("plugins", onPlugins)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				slog.Error("file watcher error", "error", err)
			}
		}
	}()
	return watcher, nil
}
