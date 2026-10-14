package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
)

func installMaintenanceFixture(t *testing.T, h *benchH, state string, key string, fingerprint string) *kbstate.MaintenanceSession {
	t.Helper()
	h.seed("kb_compta", "v1.0.0", true)
	app := h.app("kb_compta")
	original := h.receipt(app.Dir()).Receipt
	if original == nil {
		t.Fatal("seeded kb_compta receipt missing")
	}
	originalCopy := *original
	sessionID := strings.Repeat("a", 32)
	requestID := strings.Repeat("b", 32)
	keyHash := sha256.Sum256([]byte(key))
	s := &kbstate.MaintenanceSession{
		SchemaVersion: kbstate.SupportedSchema,
		SessionID:     sessionID, BenchID: strings.Repeat("c", 32),
		Fingerprint: fingerprint, LicenseKeySHA256: hex.EncodeToString(keyHash[:]),
		ClientRequestID: requestID, Reason: "repair", SourceState: state, ApprovalState: "approved",
		Apps: []kbstate.MaintenanceApp{{
			App: app.Name, Directory: app.Dir(), Package: app.PackageName(),
			Repository: "KB-Developpement/" + app.Repository(), Line: "1",
			BaseCommit: originalCopy.Commit, Branch: "main", Head: originalCopy.Commit,
			OriginalReceipt: &originalCopy,
		}},
		PatchSnapshot: map[string][]string{}, Backups: []kbstate.MaintenanceBackup{},
	}
	if state == "attaching" {
		s.Pending = &kbstate.MaintenancePending{Op: "enter", Expected: map[string]string{}}
	}
	if err := kbstate.WriteMaintenance(h.root, s); err != nil {
		t.Fatalf("write maintenance fixture: %v", err)
	}
	protectedReceipt := originalCopy
	protectedReceipt.State = "active"
	protectedReceipt.SessionID = &sessionID
	protectedReceipt.Provenance = kbstate.ProvMaintenance
	if err := kbstate.WriteReceipt(h.root, &protectedReceipt); err != nil {
		t.Fatalf("write protected receipt: %v", err)
	}
	return s
}

func TestMaintenanceCommandGateMatrix(t *testing.T) {
	h := newBenchH(t)
	states := []string{"attaching", "active", "pending_release", "returning", "recovery_needed"}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			installMaintenanceFixture(t, h, state, "original-key", strings.Repeat("d", 64))
			allowed := []struct {
				name  string
				check bool
			}{
				{name: "status"}, {name: "license"}, {name: "update", check: true},
			}
			for _, tc := range allowed {
				cmd := &cobra.Command{Use: tc.name}
				if tc.name == "update" {
					cmd.Flags().Bool("check", tc.check, "")
				}
				if err := maintenanceCommandGate(cmd); err != nil {
					t.Errorf("%s gate = %v; want allowed", tc.name, err)
				}
			}
			for _, name := range []string{"add", "install", "site-install", "init-kb-frappe", "update", "init", "config", "upgrade", "manage", "adopt"} {
				cmd := &cobra.Command{Use: name}
				if err := maintenanceCommandGate(cmd); err == nil || !strings.Contains(err.Error(), "maintenance") {
					t.Errorf("%s gate = %v; want maintenance refusal", name, err)
				}
			}
			maintenance := &cobra.Command{Use: "maintenance"}
			maintenanceStatus := &cobra.Command{Use: "status"}
			maintenance.AddCommand(maintenanceStatus)
			if err := maintenanceCommandGate(maintenanceStatus); err != nil {
				t.Errorf("maintenance status gate = %v; want allowed", err)
			}
		})
	}
}

func TestBlockedCommandsHaveNoBenchSideEffects(t *testing.T) {
	h := newBenchH(t)
	installMaintenanceFixture(t, h, "active", "original-key", strings.Repeat("d", 64))
	temp := filepath.Join(kbstate.Dir(h.root), ".gate.tmp-test")
	if err := os.WriteFile(temp, []byte("preserve until recovery"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeDigest := h.benchDigest()
	beforeRequests := h.srv.requestCount()
	beforeReceipt, err := os.ReadFile(kbstate.ReceiptPath(h.root, "kb_compta"))
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"init"}, {"config"}, {"upgrade", "--apps", "kb_compta"}, {"manage"}, {"adopt"},
	} {
		out, err := h.exec(args...)
		if err == nil || !strings.Contains(err.Error(), "maintenance") {
			t.Errorf("kb %s error = %v; output %s", strings.Join(args, " "), err, out)
		}
	}
	if got := h.benchDigest(); got != beforeDigest {
		t.Errorf("blocked command changed bench digest: before %s after %s", beforeDigest, got)
	}
	if got, err := os.ReadFile(kbstate.ReceiptPath(h.root, "kb_compta")); err != nil || string(got) != string(beforeReceipt) {
		t.Errorf("protected receipt changed: err=%v", err)
	}
	if got, err := os.ReadFile(temp); err != nil || string(got) != "preserve until recovery" {
		t.Errorf("maintenance refusal cleaned or changed temp file: %q, %v", got, err)
	}
	if got := h.srv.requestCount(); got != beforeRequests {
		t.Errorf("blocked commands made %d server requests, want none", got-beforeRequests)
	}
	if got := h.benchLogLines(); len(got) != 0 {
		t.Errorf("blocked commands ran bench commands: %v", got)
	}
	if got := h.gitCalls(); len(got) != 0 {
		t.Errorf("blocked commands ran git commands: %v", got)
	}
}

func TestReadOnlyCommandsRunWhileMaintenanceActive(t *testing.T) {
	h := newBenchH(t)
	installMaintenanceFixture(t, h, "active", "original-key", strings.Repeat("d", 64))
	for _, args := range [][]string{{"status"}, {"license"}, {"update", "--check"}} {
		out, err := h.exec(args...)
		if err != nil {
			t.Errorf("kb %s refused while maintenance active: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

func TestRunLockedRefusesBeforeCleanupReplayOrMutation(t *testing.T) {
	h := newBenchH(t)
	installMaintenanceFixture(t, h, "active", "original-key", strings.Repeat("d", 64))
	temp := filepath.Join(kbstate.Dir(h.root), ".replay.tmp-test")
	if err := os.WriteFile(temp, []byte("must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := kbstate.JournalPath(h.root)
	journalBody := []byte(`{"schema_version":`)
	if err := os.WriteFile(journal, journalBody, 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err := runLocked(context.Background(), func() error { called = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "maintenance") || called {
		t.Fatalf("runLocked err=%v fnCalled=%v; want refusal before callback", err, called)
	}
	if got, err := os.ReadFile(temp); err != nil || string(got) != "must remain" {
		t.Fatalf("temp file after refusal = %q, %v", got, err)
	}
	if got, err := os.ReadFile(journal); err != nil || string(got) != string(journalBody) {
		t.Fatalf("journal after refusal = %q, %v", got, err)
	}
	if got := h.benchLogLines(); len(got) != 0 {
		t.Fatalf("refused operation ran bench commands: %v", got)
	}
	if got := h.gitCalls(); len(got) != 0 {
		t.Fatalf("refused operation ran git commands: %v", got)
	}
}

func TestDirectMenusRefuseBeforePromptOrLicenseDeletion(t *testing.T) {
	h := newBenchH(t)
	installMaintenanceFixture(t, h, "active", "original-key", strings.Repeat("d", 64))
	if err := license.SaveLicenseKey("saved-key"); err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() { runLicenseDeactivate() })
	if !strings.Contains(out, "maintenance") {
		t.Errorf("deactivate output = %q; want maintenance refusal", out)
	}
	if got := license.LoadLicenseKey(); got != "saved-key" {
		t.Errorf("deactivation removed saved key: %q", got)
	}

	settingsPath := filepath.Join(h.home, ".config", "kb", "config.json")
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("unexpected preexisting settings file, stat err = %v", err)
	}
	menuOut := capture(t, func() { runInit(); runConfigEdit() })
	if strings.Count(menuOut, "maintenance") < 2 {
		t.Errorf("init/config output = %q; want both maintenance refusals", menuOut)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Errorf("direct settings menu wrote config, stat err = %v", err)
	}
}

func TestActivationGateRejectsWrongFingerprintOrKeyWithoutHTTP(t *testing.T) {
	h := newBenchH(t)
	fingerprint, err := license.Fingerprint()
	if err != nil {
		t.Skipf("fingerprint unavailable: %v", err)
	}
	installMaintenanceFixture(t, h, "active", "original-key", fingerprint)
	before := h.srv.requestCount()
	if err := maintenanceActivationGate("wrong-key"); err == nil {
		t.Fatal("wrong license key passed maintenance activation gate")
	}
	if got := h.srv.requestCount(); got != before {
		t.Fatalf("wrong key made %d HTTP requests", got-before)
	}

	installMaintenanceFixture(t, h, "active", "original-key", strings.Repeat("e", 64))
	if err := maintenanceActivationGate("original-key"); err == nil {
		t.Fatal("wrong fingerprint passed maintenance activation gate")
	}
	if got := h.srv.requestCount(); got != before {
		t.Fatalf("wrong fingerprint made %d HTTP requests", got-before)
	}
}
