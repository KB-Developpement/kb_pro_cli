package kbstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveMaintenanceSessionRetainsClosedOutcomes(t *testing.T) {
	root := t.TempDir()
	session := validMaintenanceSession()
	session.ApprovalState = "closed"
	session.SchemaChanged = true
	session.MigrationOutcomes = map[string]string{"site1": "ok"}
	if err := WriteMaintenance(root, session); err != nil {
		t.Fatal(err)
	}

	if err := ArchiveMaintenanceSession(root, session); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(RecoveryDir(root), session.SessionID, "session.json")
	archived, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ArchiveMaintenanceSession(root, session); err != nil {
		t.Fatalf("repeat archive: %v", err)
	}
	repeated, err := os.ReadFile(archivePath)
	if err != nil || string(repeated) != string(archived) {
		t.Fatalf("repeat archive changed bytes: err=%v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(archived, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["license_key"]; exists {
		t.Fatal("archived session contains plaintext license_key field")
	}
	var outcomes map[string]string
	if err := json.Unmarshal(fields["migration_outcomes"], &outcomes); err != nil || outcomes["site1"] != "ok" {
		t.Fatalf("archived migration outcomes = %v, err = %v", outcomes, err)
	}

	for _, dir := range []string{Dir(root), RecoveryDir(root), filepath.Dir(archivePath)} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s mode = %v, err = %v; want 0700", dir, info, err)
		}
	}
	info, err := os.Stat(archivePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode = %v, err = %v; want 0600", info, err)
	}

	newSession := validMaintenanceSession()
	newSession.SessionID = "dddddddddddddddddddddddddddddddd"
	newSession.ClientRequestID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	newSession.Apps[0].ProtectionRefs[0] = "refs/kb-maintenance/" + newSession.SessionID + "/" + newSession.Apps[0].Protected[0]
	newSession.ApprovalState = "approved"
	newSession.SchemaChanged = false
	newSession.MigrationOutcomes = nil
	if err := WriteMaintenance(root, newSession); err != nil {
		t.Fatal(err)
	}
	afterNewCurrent, err := os.ReadFile(archivePath)
	if err != nil || string(afterNewCurrent) != string(archived) {
		t.Fatalf("writing later current request changed prior archive: err=%v", err)
	}
	var retained MaintenanceSession
	if err := json.Unmarshal(afterNewCurrent, &retained); err != nil || retained.MigrationOutcomes["site1"] != "ok" {
		t.Fatalf("prior archived outcome missing after later request: outcomes=%v err=%v", retained.MigrationOutcomes, err)
	}
}

func TestArchiveMaintenanceSessionDoesNotReplaceConflict(t *testing.T) {
	root := t.TempDir()
	session := validMaintenanceSession()
	session.ApprovalState = "closed"
	session.SchemaChanged = true
	session.MigrationOutcomes = map[string]string{"site1": "ok"}
	dir := filepath.Join(RecoveryDir(root), session.SessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	original := []byte("conflicting existing record\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ArchiveMaintenanceSession(root, session); err == nil {
		t.Fatal("conflicting existing record was accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("conflicting record changed: got %q, err=%v", got, err)
	}
}

func TestArchiveMaintenanceSessionRejectsMatchingSymlink(t *testing.T) {
	root := t.TempDir()
	session := validMaintenanceSession()
	session.ApprovalState = "closed"
	session.SchemaChanged = true
	session.MigrationOutcomes = map[string]string{"site1": "ok"}
	raw, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')

	dir := filepath.Join(RecoveryDir(root), session.SessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "sentinel.json")
	if err := os.WriteFile(sentinel, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	if err := os.Symlink(sentinel, path); err != nil {
		t.Fatal(err)
	}
	if err := ArchiveMaintenanceSession(root, session); err == nil {
		t.Fatal("matching symlink was accepted as an existing archive")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("archive path changed from symlink: mode=%v err=%v", info, err)
	}
	gotTarget, err := os.Readlink(path)
	if err != nil || gotTarget != sentinel {
		t.Fatalf("symlink target = %q, err=%v; want %q", gotTarget, err, sentinel)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("sentinel changed: err=%v", err)
	}
}

func TestArchiveMaintenanceSessionRejectsMatchingWrongModeFile(t *testing.T) {
	root := t.TempDir()
	session := validMaintenanceSession()
	session.ApprovalState = "closed"
	session.SchemaChanged = true
	session.MigrationOutcomes = map[string]string{"site1": "ok"}
	raw, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	dir := filepath.Join(RecoveryDir(root), session.SessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ArchiveMaintenanceSession(root, session); err == nil {
		t.Fatal("matching archive with wrong mode was accepted")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("existing archive mode changed: mode=%v err=%v; want 0644", info, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("existing archive bytes changed: err=%v", err)
	}
}

func TestArchiveMaintenanceSessionRejectsIncompleteOrInvalidRecords(t *testing.T) {
	cases := []struct {
		name   string
		change func(*MaintenanceSession)
	}{
		{"malformed session", func(s *MaintenanceSession) { s.Fingerprint = "bad" }},
		{"invalid migration outcome", func(s *MaintenanceSession) { s.MigrationOutcomes = map[string]string{"site1": "partial"} }},
		{"schema changed flag missing", func(s *MaintenanceSession) { s.SchemaChanged = false }},
		{"not closed", func(s *MaintenanceSession) { s.ApprovalState = "approved" }},
		{"pending operation", func(s *MaintenanceSession) { s.Pending = &MaintenancePending{Op: "finish"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			session := validMaintenanceSession()
			session.ApprovalState = "closed"
			session.SchemaChanged = true
			session.MigrationOutcomes = map[string]string{"site1": "ok"}
			tc.change(session)
			if err := ArchiveMaintenanceSession(root, session); err == nil {
				t.Fatal("invalid or incomplete session was archived")
			}
			archivePath := filepath.Join(RecoveryDir(root), session.SessionID, "session.json")
			if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
				t.Fatalf("refused archive left a record: stat err=%v", err)
			}
		})
	}
}

func TestArchiveMaintenanceSessionRequiresTopLevelMigrationOutcomes(t *testing.T) {
	root := t.TempDir()
	session := validMaintenanceSession()
	session.ApprovalState = "closed"
	session.SchemaChanged = true
	session.MigrationOutcomes = map[string]string{"site1": "ok"}
	pendingShape := *session
	pendingShape.Pending = &MaintenancePending{Op: "finish"}
	raw, err := json.Marshal(&pendingShape)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["migration_outcomes"]; !ok {
		t.Fatal("migration_outcomes missing from top level")
	}
	var pending map[string]json.RawMessage
	if err := json.Unmarshal(fields["pending"], &pending); err != nil {
		t.Fatal(err)
	}
	if _, ok := pending["migrations"]; ok {
		t.Fatal("migration outcomes remain nested under pending")
	}
	if err := ArchiveMaintenanceSession(root, session); err != nil {
		t.Fatal(err)
	}
}
