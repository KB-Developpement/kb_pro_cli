package config

import (
	"os"
	"path/filepath"
	"strings"
)

const tokenFile = "github_token"

// LoadToken returns the GitHub personal access token.
// KB_GITHUB_TOKEN env var takes precedence over the file at ~/.config/kb/github_token.
// Returns an empty string if neither is set.
func LoadToken() string {
	if v := os.Getenv("KB_GITHUB_TOKEN"); v != "" {
		return strings.TrimSpace(v)
	}
	data, err := os.ReadFile(filepath.Join(ConfigDir(), tokenFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveToken writes the token to ~/.config/kb/github_token (mode 0600).
func SaveToken(token string) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, tokenFile), []byte(token+"\n"), 0600)
}
