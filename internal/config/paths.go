package config

import (
	"os"
	"path/filepath"
)

// ConfigDir returns ~/.config/kb — where the update-check cache is stored.
func ConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "kb")
}
