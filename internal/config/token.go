package config

import (
	"os"
	"strings"
)

// LoadToken returns the GitHub personal access token.
// Precedence: KB_GITHUB_TOKEN env → github_token in ~/.config/kb/config.json.
// Returns an empty string if none is set.
func LoadToken() string {
	if v := os.Getenv("KB_GITHUB_TOKEN"); v != "" {
		return strings.TrimSpace(v)
	}
	s, err := LoadStoredSettings()
	if err == nil && strings.TrimSpace(s.GitHubToken) != "" {
		return strings.TrimSpace(s.GitHubToken)
	}
	return ""
}

// SaveToken persists the token into config.json (merging with existing stored settings).
func SaveToken(token string) error {
	s, err := LoadStoredSettings()
	if err != nil {
		return err
	}
	s.GitHubToken = strings.TrimSpace(token)
	return SaveStoredSettings(s)
}
