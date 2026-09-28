package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang-mail/go-mail/cache"
)

// filtersStatePath holds the digest of filters.yml as of the last full run.
func filtersStatePath() string {
	return cache.ConfigPath(filepath.Join("db", "filters_last_full_run"))
}

func filtersDigest(filtersYAML []byte) string {
	sum := sha256.Sum256(filtersYAML)
	return hex.EncodeToString(sum[:])
}

// fullRunNeeded reports whether filters changed since the last full pass.
// A missing or unreadable state file always asks for a full run.
func fullRunNeeded(statePath string, filtersYAML []byte) bool {
	prev, err := os.ReadFile(statePath)
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(prev)) != filtersDigest(filtersYAML)
}

func recordFullRun(statePath string, filtersYAML []byte) error {
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return err
	}
	return cache.WriteFileAtomic(statePath, []byte(filtersDigest(filtersYAML)+"\n"), 0o600)
}
