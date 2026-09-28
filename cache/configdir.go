package cache

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDir is the base directory for all configuration and data files.
// Set by main() before any other initialization.
var ConfigDir string

// InitConfigDir sets ConfigDir to ~/.go-mail and creates the directory if needed.
func InitConfigDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	ConfigDir = filepath.Join(home, ".go-mail")
	return os.MkdirAll(ConfigDir, 0o755)
}

// ConfigPath returns the full path for a file within the config directory.
func ConfigPath(name string) string {
	return filepath.Join(ConfigDir, name)
}
