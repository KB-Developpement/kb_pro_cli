//go:build !windows

package kbstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestArchiveMaintenanceSessionUsesExactModeUnderRestrictiveUmask(t *testing.T) {
	const (
		helperEnv = "KBSTATE_ARCHIVE_UMASK_HELPER"
		rootEnv   = "KBSTATE_ARCHIVE_UMASK_ROOT"
	)
	if os.Getenv(helperEnv) == "1" {
		syscall.Umask(0o777)
		session := validMaintenanceSession()
		session.ApprovalState = "closed"
		session.SchemaChanged = true
		session.MigrationOutcomes = map[string]string{"site1": "ok"}
		if err := ArchiveMaintenanceSession(os.Getenv(rootEnv), session); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(RecoveryDir(os.Getenv(rootEnv)), session.SessionID, "session.json")
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("archive mode under umask 0777 = %v, err=%v; want 0600", info, err)
		}
		return
	}

	root := t.TempDir()
	t.Setenv(helperEnv, "1")
	t.Setenv(rootEnv, root)
	cmd := exec.Command(os.Args[0], "-test.run=^TestArchiveMaintenanceSessionUsesExactModeUnderRestrictiveUmask$")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("umask helper failed: %v\n%s", err, output)
	}
}
