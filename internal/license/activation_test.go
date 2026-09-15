package license

import (
	"os"
	"strings"
	"testing"
)

func TestActivationLimitHelp_NamesTheOldActivation(t *testing.T) {
	help := ActivationLimitHelp("KB-ACME-123", "aaaabbbbccccdddd")

	for _, want := range []string{
		"upgrading kb",
		"kbls remove-activation --key KB-ACME-123 --fingerprint aaaabbbbccccdddd",
		"--max-activations",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("ActivationLimitHelp missing %q:\n%s", want, help)
		}
	}
}

func TestActivationLimitHelp_PlaceholdersWhenUnknown(t *testing.T) {
	help := ActivationLimitHelp("", "")

	if !strings.Contains(help, "kbls remove-activation --key <license-key> --fingerprint <old-fingerprint>") {
		t.Errorf("ActivationLimitHelp should fall back to placeholders:\n%s", help)
	}
}

func TestPreviousFingerprint_FromStashedValue(t *testing.T) {
	withTempConfigDir(t)

	if got := PreviousFingerprint(); got != "" {
		t.Errorf("PreviousFingerprint = %q, want empty with nothing on disk", got)
	}

	savePreviousFingerprint("feedface")
	if got := PreviousFingerprint(); got != "feedface" {
		t.Errorf("PreviousFingerprint = %q, want feedface", got)
	}

	info, err := os.Stat(previousFingerprintPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("previous-fingerprint permissions: got %04o, want 0600", perm)
	}

	ClearPreviousFingerprint()
	if got := PreviousFingerprint(); got != "" {
		t.Errorf("PreviousFingerprint = %q after clear, want empty", got)
	}
}
