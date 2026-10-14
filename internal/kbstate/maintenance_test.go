package kbstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validMaintenanceSession() *MaintenanceSession {
	const (
		sessionID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		benchID   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		requestID = "cccccccccccccccccccccccccccccccc"
		commit    = "0123456789abcdef0123456789abcdef01234567"
	)
	tag := "v1.0.0"
	return &MaintenanceSession{
		SchemaVersion: SupportedSchema,
		SessionID:     sessionID, BenchID: benchID,
		Fingerprint: strings.Repeat("d", 64), LicenseKeySHA256: strings.Repeat("e", 64),
		ClientRequestID: requestID, Reason: "repair", SourceState: StateArchive, ApprovalState: "approved",
		Apps: []MaintenanceApp{{
			App: "kb_compta", Directory: "kb_compta", Package: "kb_compta",
			Repository: "KB-Developpement/kb_compta", Line: "1", BaseCommit: commit,
			Branch: "main", Head: commit, Protected: []string{commit},
			ProtectionRefs: []string{"refs/kb-maintenance/" + sessionID + "/" + commit},
			OriginalReceipt: &Receipt{
				SchemaVersion: SupportedSchema, App: "kb_compta", Directory: "kb_compta", Package: "kb_compta",
				Repository: "KB-Developpement/kb_compta", Line: "1", Requested: "major=1", Ref: tag,
				Commit: commit, ReleaseTag: &tag, ArchiveSHA256: strings.Repeat("f", 64),
				InstalledAt: "2026-01-01T00:00:00Z", State: StateArchive, Provenance: ProvInstalled,
			},
		}},
		PatchSnapshot: map[string][]string{}, Backups: []MaintenanceBackup{}, LastSeq: 0,
	}
}

func TestEnsureBenchIDPersistsIdentityExclusively(t *testing.T) {
	root := t.TempDir()
	first, err := EnsureBenchID(root)
	if err != nil {
		t.Fatal(err)
	}
	if !MaintenanceIDRE.MatchString(first) {
		t.Fatalf("bench ID = %q, want 32 lowercase hex characters", first)
	}
	second, err := EnsureBenchID(root)
	if err != nil || second != first {
		t.Fatalf("second bench ID = %q, %v; want stable ID %q", second, err, first)
	}
	path := filepath.Join(Dir(root), "bench-id")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("bench identity mode = %v, err = %v; want 0600", info, err)
	}
	kbInfo, err := os.Stat(Dir(root))
	if err != nil || kbInfo.Mode().Perm() != 0o700 {
		t.Fatalf(".kb mode = %v, err = %v; want 0700", kbInfo, err)
	}
}

func TestEnsureBenchIDDoesNotReplaceMalformedIdentity(t *testing.T) {
	root := t.TempDir()
	if err := EnsureKB(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(root), "bench-id")
	original := []byte("{broken identity\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureBenchID(root); err == nil {
		t.Fatal("malformed existing identity was accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("malformed identity changed: %q, err = %v", got, err)
	}
}

func TestWriteMaintenanceUsesPrivateDurableFile(t *testing.T) {
	root := t.TempDir()
	s := validMaintenanceSession()
	if err := WriteMaintenance(root, s); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMaintenance(root)
	if err != nil || got == nil || got.SessionID != s.SessionID {
		t.Fatalf("read maintenance = %+v, %v", got, err)
	}
	info, err := os.Stat(MaintenancePath(root))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session mode = %v, err = %v; want 0600", info, err)
	}
	dirInfo, err := os.Stat(filepath.Dir(MaintenancePath(root)))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("maintenance directory mode = %v, err = %v; want 0700", dirInfo, err)
	}
}

func TestMaintenanceSessionValidateRejectsUnsafeState(t *testing.T) {
	cases := []struct {
		name   string
		change func(*MaintenanceSession)
	}{
		{"wrong protection ref", func(s *MaintenanceSession) { s.Apps[0].ProtectionRefs[0] = "refs/heads/main" }},
		{"mismatched protected count", func(s *MaintenanceSession) { s.Apps[0].ProtectionRefs = nil }},
		{"unsafe backup path", func(s *MaintenanceSession) {
			s.Backups = []MaintenanceBackup{{Site: "site1", Path: "../outside", TakenAt: "2026-01-01T00:00:00Z"}}
		}},
		{"recovery outside recovery tree", func(s *MaintenanceSession) { s.Recovery = []string{"apps/kb_compta/file.py"} }},
		{"pending recovery outside recovery tree", func(s *MaintenanceSession) { s.Pending = &MaintenancePending{Op: "recovery", Recovery: "apps/file.py"} }},
		{"prepared git outside kb tree", func(s *MaintenanceSession) {
			s.Pending = &MaintenancePending{Op: "enter", PreparedGit: "apps/prepared"}
		}},
		{"pending report sequence gap", func(s *MaintenanceSession) {
			s.PendingReport = &PendingReport{Seq: 2, Body: json.RawMessage(`{"ok":true}`)}
		}},
		{"malformed pending report body", func(s *MaintenanceSession) { s.PendingReport = &PendingReport{Seq: 1, Body: json.RawMessage(`{`)} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validMaintenanceSession()
			tc.change(s)
			if err := s.Validate(); err == nil {
				t.Fatal("invalid maintenance session was accepted")
			}
		})
	}
	for _, state := range []string{"archive", "attaching", "active", "pending_release", "returning", "recovery_needed"} {
		t.Run("state "+state, func(t *testing.T) {
			s := validMaintenanceSession()
			s.SourceState = state
			if err := s.Validate(); err != nil {
				t.Fatalf("valid state %q rejected: %v", state, err)
			}
		})
	}
}

func TestReadMaintenanceMalformedFailsClosed(t *testing.T) {
	root := t.TempDir()
	path := MaintenancePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := ReadMaintenance(root); err == nil || s != nil || !strings.Contains(err.Error(), "recovery_needed") {
		t.Fatalf("ReadMaintenance = %+v, %v; want fail-closed recovery_needed error", s, err)
	}
}

func TestMaintenanceGateUsesReceiptProtectionWhenSessionMissing(t *testing.T) {
	root := t.TempDir()
	if err := EnsureTree(root, ReceiptsDir(root)); err != nil {
		t.Fatal(err)
	}
	tag := "v1.0.0"
	sessionID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r := &Receipt{
		SchemaVersion: SupportedSchema, App: "kb_compta", Directory: "kb_compta", Package: "kb_compta",
		Repository: "KB-Developpement/kb_compta", Line: "1", Requested: "major=1", Ref: tag,
		Commit: "0123456789abcdef0123456789abcdef01234567", ReleaseTag: &tag,
		ArchiveSHA256: strings.Repeat("a", 64), InstalledAt: "2026-01-01T00:00:00Z",
		State: "active", SessionID: &sessionID, Provenance: ProvMaintenance,
	}
	if err := WriteReceipt(root, r); err != nil {
		t.Fatal(err)
	}
	if err := MaintenanceGate(root); err == nil || !strings.Contains(err.Error(), "maintenance receipt") {
		t.Fatalf("gate without session = %v; want receipt protection error", err)
	}
}
