package license

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestFingerprint_Stable(t *testing.T) {
	withTempConfigDir(t)

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
	if _, err := hex.DecodeString(fp1); err != nil {
		t.Errorf("Fingerprint is not hex: %q", fp1)
	}
	if fp1 != strings.ToLower(fp1) {
		t.Errorf("Fingerprint must be lowercase hex: %q", fp1)
	}
}

// Two installations on the same host — same /etc/machine-id, same CPU — must
// not share an identity. This is the container case: the image bakes
// /etc/machine-id in, so host signals alone collapse to one constant.
func TestFingerprint_DiffersPerConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fp1, err := Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint (install 1): %v", err)
	}

	t.Setenv("HOME", t.TempDir())
	fp2, err := Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint (install 2): %v", err)
	}

	if fp1 == fp2 {
		t.Errorf("two installations share a fingerprint: %q", fp1)
	}
}

func TestFingerprint_CreatesInstallIDOnce(t *testing.T) {
	withTempConfigDir(t)

	fp1, err := Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}

	info, err := os.Stat(installIDPath())
	if err != nil {
		t.Fatalf("install id file not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("install id permissions: got %04o, want 0600", perm)
	}

	first, err := os.ReadFile(installIDPath())
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(first))
	raw, err := hex.DecodeString(id)
	if err != nil {
		t.Fatalf("install id is not hex: %q", id)
	}
	if len(raw) != 16 {
		t.Errorf("install id is %d bytes, want 16 (128 bits)", len(raw))
	}

	fp2, err := Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint (2nd call): %v", err)
	}
	second, err := os.ReadFile(installIDPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(first) {
		t.Errorf("install id was rewritten: %q -> %q", first, second)
	}
	if fp1 != fp2 {
		t.Errorf("fingerprint changed across calls: %q != %q", fp1, fp2)
	}
}

func TestFingerprint_HonoursExistingInstallID(t *testing.T) {
	withTempConfigDir(t)

	const existing = "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(installIDPath(), []byte(existing+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fp, err := Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}

	want := sha256.Sum256([]byte(existing + "|" + hostMachineID() + "|" + cpuModel()))
	if fp != hex.EncodeToString(want[:]) {
		t.Errorf("fingerprint = %q, want %q", fp, hex.EncodeToString(want[:]))
	}

	data, err := os.ReadFile(installIDPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != existing {
		t.Errorf("pre-existing install id was replaced: %q", got)
	}
}

// A missing /etc/machine-id is no longer an error — it just drops out of the mix.
func TestHostMachineID_NeverErrors(t *testing.T) {
	id := hostMachineID()
	if strings.ContainsAny(id, "\n\r") {
		t.Errorf("hostMachineID contains a newline: %q", id)
	}
}

func TestCPUModel(t *testing.T) {
	model := cpuModel()
	// On Linux this should return something; on other systems it may be empty.
	// Just verify it doesn't panic and isn't clearly wrong.
	if strings.Contains(model, "\n") {
		t.Errorf("cpuModel contains newline: %q", model)
	}
}
