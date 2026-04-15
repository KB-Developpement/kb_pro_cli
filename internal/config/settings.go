package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const settingsFile = "config.json"

// DefaultLicenseServerURL is used when no URL is stored or overridden by env.
const DefaultLicenseServerURL = "https://license.kbdev.co"

// StoredSettings holds values written by "kb init" / "kb config" (config.json).
// Environment variables (KB_GITHUB_TOKEN, KB_LICENSE_SERVER) still override at
// runtime via LoadToken / ResolveLicenseServerURL — they are not persisted here.
type StoredSettings struct {
	LicenseServerURL string `json:"license_server_url"`
	GitHubToken      string `json:"github_token"`
}

// SettingsPath returns the path to ~/.config/kb/config.json.
func SettingsPath() string {
	return filepath.Join(ConfigDir(), settingsFile)
}

// IsInitialized reports whether ~/.config/kb/config.json exists.
// Legacy sidecar files are ignored; users must run kb init to create config.json.
func IsInitialized() bool {
	_, err := os.Stat(SettingsPath())
	return err == nil
}

// LoadStoredSettings reads persisted settings from config.json only.
// It does not read environment variables (use LoadToken / ResolveLicenseServerURL for runtime).
// A missing file returns zero StoredSettings and no error.
func LoadStoredSettings() (StoredSettings, error) {
	path := SettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StoredSettings{}, nil
		}
		return StoredSettings{}, fmt.Errorf("read settings: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return StoredSettings{}, nil
	}
	var s StoredSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return StoredSettings{}, fmt.Errorf("parse settings: %w", err)
	}
	return s, nil
}

// SaveStoredSettings writes settings to config.json (mode 0600 because of the token).
func SaveStoredSettings(s StoredSettings) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(SettingsPath(), data, 0600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}

// ResolveLicenseServerURL returns the license server base URL using precedence:
//  1. KB_LICENSE_SERVER environment variable
//  2. Stored license_server_url in config.json
//  3. DefaultLicenseServerURL
func ResolveLicenseServerURL() string {
	if v := os.Getenv("KB_LICENSE_SERVER"); strings.TrimSpace(v) != "" {
		return strings.TrimRight(strings.TrimSpace(v), "/")
	}
	s, err := LoadStoredSettings()
	if err == nil && strings.TrimSpace(s.LicenseServerURL) != "" {
		return strings.TrimRight(strings.TrimSpace(s.LicenseServerURL), "/")
	}
	return DefaultLicenseServerURL
}
