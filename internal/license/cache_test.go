package license

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withTempConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, ".config", "kb"), 0700)
}

func TestSaveAndLoadCache(t *testing.T) {
	withTempConfigDir(t)

	entry := &cacheEntry{
		Token:       "eyJtest",
		ActivatedAt: time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC),
		LastCheck:   time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC),
	}
	if err := saveCache(entry); err != nil {
		t.Fatalf("saveCache: %v", err)
	}

	// Verify 0600 permissions
	info, err := os.Stat(cachePath())
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("cache permissions: got %04o, want 0600", perm)
	}

	loaded, err := loadCache()
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	if loaded == nil {
		t.Fatal("loadCache returned nil for existing file")
	}
	if loaded.Token != "eyJtest" {
		t.Errorf("Token: got %q, want eyJtest", loaded.Token)
	}
}

func TestLoadCache_Missing(t *testing.T) {
	withTempConfigDir(t)
	entry, err := loadCache()
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	if entry != nil {
		t.Error("expected nil for missing cache file")
	}
}

func TestDeleteCache(t *testing.T) {
	withTempConfigDir(t)
	_ = saveCache(&cacheEntry{Token: "tok"})
	deleteCache()
	entry, _ := loadCache()
	if entry != nil {
		t.Error("expected nil after deleteCache")
	}
}

func TestSaveAndLoadLicenseKey(t *testing.T) {
	withTempConfigDir(t)
	if err := SaveLicenseKey("mykey"); err != nil {
		t.Fatalf("SaveLicenseKey: %v", err)
	}
	got := LoadLicenseKey()
	if got != "mykey" {
		t.Errorf("LoadLicenseKey: got %q, want mykey", got)
	}

	// Verify 0600 permissions
	info, err := os.Stat(keyPath())
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("key file permissions: got %04o, want 0600", perm)
	}
}

func TestLoadLicenseKey_Missing(t *testing.T) {
	withTempConfigDir(t)
	got := LoadLicenseKey()
	if got != "" {
		t.Errorf("expected empty for missing key file, got %q", got)
	}
}
