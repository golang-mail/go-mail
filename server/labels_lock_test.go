package server

import (
	"fmt"
	"sync"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func TestEnsureLabel_ConcurrentCallsKeepEveryLabel(t *testing.T) {
	old := cache.ConfigDir
	cache.ConfigDir = t.TempDir()
	t.Cleanup(func() { cache.ConfigDir = old })

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := EnsureLabel(fmt.Sprintf("label-%d", i), "local"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	cfg, err := ReadLabels()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Labels) != n {
		t.Fatalf("want %d labels, got %d (lost updates)", n, len(cfg.Labels))
	}
}
