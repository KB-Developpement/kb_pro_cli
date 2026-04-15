package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoredSettingsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := StoredSettings{
		LicenseServerURL: "https://license.example.test",
		GitHubToken:      "ghp_testtoken",
	}
	if err := SaveStoredSettings(s); err != nil {
		t.Fatalf("SaveStoredSettings: %v", err)
	}

	got, err := LoadStoredSettings()
	if err != nil {
		t.Fatalf("LoadStoredSettings: %v", err)
	}
	if got.LicenseServerURL != s.LicenseServerURL || got.GitHubToken != s.GitHubToken {
		t.Fatalf("LoadStoredSettings: got %+v want %+v", got, s)
	}
	if !IsInitialized() {
		t.Fatal("IsInitialized: want true after SaveStoredSettings")
	}
}

func TestIsInitializedIgnoresLegacyGitHubTokenFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	legacyToken := filepath.Join(dir, "github_token")
	if err := os.WriteFile(legacyToken, []byte("legacy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if IsInitialized() {
		t.Fatal("IsInitialized: want false when only legacy github_token exists (config.json required)")
	}
}

func TestResolveLicenseServerURLStored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KB_LICENSE_SERVER", "")

	if err := SaveStoredSettings(StoredSettings{LicenseServerURL: "https://custom.example"}); err != nil {
		t.Fatal(err)
	}
	if got := ResolveLicenseServerURL(); got != "https://custom.example" {
		t.Fatalf("ResolveLicenseServerURL: got %q", got)
	}
}
