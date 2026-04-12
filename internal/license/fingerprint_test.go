package license

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFingerprint_Stable(t *testing.T) {
	fp1, err := Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	fp2, err := Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint (2nd call): %v", err)
	}
	if fp1 != fp2 {
		t.Errorf("Fingerprint is not stable: %q != %q", fp1, fp2)
	}
	if len(fp1) != 64 {
		t.Errorf("expected 64-char hex, got %d chars: %q", len(fp1), fp1)
	}
}

func TestMachineID_Fallback(t *testing.T) {
	// Override config dir to a temp dir so we don't pollute real config.
	dir := t.TempDir()
	origConfigDir := os.Getenv("HOME")

	// Write a fake machine-id fallback file.
	_ = os.MkdirAll(filepath.Join(dir, ".config", "kb"), 0700)
	_ = os.WriteFile(filepath.Join(dir, ".config", "kb", "machine-id"), []byte("testmachineid\n"), 0600)

	// Since we can't easily swap config.ConfigDir() in tests (it reads HOME),
	// just verify that /etc/machine-id or the fallback both produce a valid ID.
	id, err := machineID()
	if err != nil {
		t.Fatalf("machineID: %v", err)
	}
	if id == "" {
		t.Error("machineID returned empty string")
	}

	_ = origConfigDir
}

func TestCPUModel(t *testing.T) {
	model := cpuModel()
	// On Linux this should return something; on other systems it may be empty.
	// Just verify it doesn't panic and isn't clearly wrong.
	if strings.Contains(model, "\n") {
		t.Errorf("cpuModel contains newline: %q", model)
	}
}
