package license

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/config"
)

const (
	licenseCacheFile = "license.json"
	licenseKeyFile   = "license_key"
)

// cacheEntry is the structure written to ~/.config/kb/license.json.
type cacheEntry struct {
	Token       string    `json:"token"`
	ActivatedAt time.Time `json:"activated_at"`
	LastCheck   time.Time `json:"last_check"`
}

// loadCache reads the license cache from disk.
// Returns nil, nil if the file does not exist.
func loadCache() (*cacheEntry, error) {
	data, err := os.ReadFile(cachePath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read license cache: %w", err)
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("parse license cache: %w", err)
	}
	return &e, nil
}

// saveCache writes the license cache to disk with 0600 permissions.
func saveCache(e *cacheEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal license cache: %w", err)
	}
	dir := config.ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(cachePath(), data, 0600); err != nil {
		return fmt.Errorf("write license cache: %w", err)
	}
	return nil
}

// deleteCache removes the license cache file.
func deleteCache() {
	_ = os.Remove(cachePath())
}

// LoadLicenseKey reads the stored license key from ~/.config/kb/license_key.
// Returns empty string if not found.
func LoadLicenseKey() string {
	data, err := os.ReadFile(keyPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveLicenseKey writes the license key to ~/.config/kb/license_key with 0600 permissions.
func SaveLicenseKey(key string) error {
	dir := config.ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(keyPath(), []byte(key+"\n"), 0600); err != nil {
		return fmt.Errorf("write license key: %w", err)
	}
	return nil
}

// SaveTokenCache writes a fresh JWT to the license cache.
// Called by kb activate after a successful activation.
func SaveTokenCache(token string, activatedAt time.Time) error {
	entry := &cacheEntry{
		Token:       token,
		ActivatedAt: activatedAt,
		LastCheck:   activatedAt,
	}
	return saveCache(entry)
}

func cachePath() string {
	return filepath.Join(config.ConfigDir(), licenseCacheFile)
}

func keyPath() string {
	return filepath.Join(config.ConfigDir(), licenseKeyFile)
}
