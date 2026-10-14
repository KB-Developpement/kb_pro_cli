package bench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/fault"
	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
)

// MaintenanceReturnPaths are recorded by the session owner before this
// transaction starts. Both source sets are retained after migration begins.
func MaintenanceReturnPaths(recovery string) (old, new string) {
	return recovery + ".maintenance-source", recovery + ".release-source"
}

// ReturnMaintenanceArchive owns source application only. The caller owns
// receipts and session state, holds the bench lock, and has verified standing,
// containment, backups, affected sites and the operator service procedure.
// record must durably persist every step; migrate/unknown precedes execution.
func ReturnMaintenanceArchive(ctx context.Context, root string, app apps.App, download *license.Download, recovery string, schemaChanged bool, sites []string, record func(step, site, outcome string) error) error {
	if record == nil {
		return errors.New("maintenance return requires durable recording")
	}
	if download == nil {
		return errors.New("maintenance return has no archive")
	}
	if download.Repository != "KB-Developpement/"+app.Repository() || !ident.CommitRE.MatchString(download.Commit) || download.ReleaseTag == "" {
		return errors.New("return archive has no matching strict release proof")
	}
	if len(sites) == 0 {
		return errors.New("maintenance return has no affected sites")
	}
	validatedSites := map[string]bool{}
	for _, site := range sites {
		if !maintenanceComponent(site) || validatedSites[site] {
			return errors.New("invalid or duplicate migration site")
		}
		validatedSites[site] = true
	}
	live, err := maintenanceAppPath(root, app.Dir())
	if err != nil {
		return err
	}
	if !filepath.IsAbs(recovery) || !withinDir(filepath.Join(filepath.Dir(filepath.Dir(live)), ".kb", "recovery"), recovery) {
		return errors.New("maintenance recovery is outside retained recovery tree")
	}
	if err = ownMaintenanceGit(live); err != nil {
		return err
	}
	if err = VerifyMaintenanceRecovery(live, recovery, recovery+".manifest.json"); err != nil {
		return fmt.Errorf("verify recovery before return: %w", err)
	}
	sum, err := fileSHA256(download.Path)
	if err != nil {
		return err
	}
	if sum != download.SHA256 {
		return errors.New("downloaded archive changed on disk")
	}
	old, staged := MaintenanceReturnPaths(recovery)
	for _, p := range []string{old, staged, recovery + ".failed-release"} {
		if _, err = os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("maintenance return path already exists or is inaccessible: %s", p)
		}
	}
	if err = record("planned", "", "unknown"); err != nil {
		return err
	}
	if err = os.Mkdir(staged, 0755); err != nil {
		return err
	}
	if err = extractTarGzStripped(download.Path, staged); err != nil {
		return fmt.Errorf("extract release (staged source retained at %s): %w", staged, err)
	}
	if _, e := os.Lstat(filepath.Join(staged, ".git")); !errors.Is(e, os.ErrNotExist) {
		return errors.New("release archive contains Git metadata")
	}
	if err = validateArchiveIdentity(staged, app.Dir(), app.PackageName()); err != nil {
		return err
	}
	if err = syncMaintenanceTree(staged); err != nil {
		return err
	}
	if err = VerifyMaintenanceRecovery(live, recovery, recovery+".manifest.json"); err != nil {
		return err
	}
	fault.Hit("KP-EXIT-0")
	if err = record("swapped", "", "unknown"); err != nil {
		return err
	}
	if err = maintenanceRenameExclusive(live, old); err != nil {
		return err
	}
	migrated := false
	rollback := func(cause error) error {
		if schemaChanged || migrated {
			return fmt.Errorf("%w; recovery needed, release source at %s, maintenance source at %s, verified copy at %s", cause, live, old, recovery)
		}
		// Keep the failed release too. Restore by rename, then prove every byte.
		if exists(live) {
			if e := maintenanceRenameExclusive(live, recovery+".failed-release"); e != nil {
				return fmt.Errorf("%w; rollback failed retaining release: %v", cause, e)
			}
		}
		if e := maintenanceRenameExclusive(old, live); e != nil {
			return fmt.Errorf("%w; rollback failed restoring maintenance: %v", cause, e)
		}
		if e := fsutil.SyncDir(filepath.Dir(live)); e != nil {
			return fmt.Errorf("%w; rollback sync failed: %v", cause, e)
		}
		if e := VerifyMaintenanceRecovery(live, recovery, recovery+".manifest.json"); e != nil {
			return fmt.Errorf("%w; rollback verification failed: %v", cause, e)
		}
		if _, e := pipInstallEditableAt(ctx, root, app.Dir()); e != nil {
			return fmt.Errorf("%w; source restored, editable package restoration failed: %v", cause, e)
		}
		if e := VerifyMaintenanceRecovery(live, recovery, recovery+".manifest.json"); e != nil {
			return fmt.Errorf("%w; editable package restoration changed source: %v", cause, e)
		}
		if e := record("rollback", "", "ok"); e != nil {
			return fmt.Errorf("%w; source restored, rollback recording failed: %v", cause, e)
		}
		return fmt.Errorf("%w; maintenance source restored and verified", cause)
	}
	if err = maintenanceRenameExclusive(staged, live); err != nil {
		return rollback(err)
	}
	if err = fsutil.SyncDir(filepath.Dir(live)); err != nil {
		return rollback(err)
	}
	if err = fsutil.SyncDir(filepath.Dir(old)); err != nil {
		return rollback(err)
	}
	if err = record("swapped", "", "ok"); err != nil {
		return rollback(err)
	}
	for _, s := range []struct {
		name string
		run  func() (string, error)
	}{
		{"setup", func() (string, error) {
			out, e := maintenanceBench(ctx, root, "setup", "requirements", "--python", app.PackageName())
			if e != nil {
				return out, e
			}
			return maintenanceBench(ctx, root, "setup", "requirements", "--node", app.PackageName())
		}},
		{"pip", func() (string, error) { return pipInstallEditableAt(ctx, root, app.Dir()) }},
		{"build", func() (string, error) { return maintenanceBench(ctx, root, "build", "--app", app.PackageName()) }},
	} {
		if err = record(s.name, "", "unknown"); err != nil {
			return rollback(err)
		}
		_, err = s.run()
		outcome := "ok"
		if err != nil {
			outcome = "failed"
		}
		if e := record(s.name, "", outcome); e != nil {
			return rollback(e)
		}
		if err != nil {
			return rollback(fmt.Errorf("%s failed: %w", s.name, err))
		}
	}
	seen := map[string]bool{}
	for _, site := range sites {
		if !maintenanceComponent(site) || seen[site] {
			return rollback(errors.New("invalid or duplicate migration site"))
		}
		seen[site] = true
		if err = record("migrate", site, "unknown"); err != nil {
			return rollback(err)
		}
		migrated = true
		cmd := exec.CommandContext(ctx, "bench", "--site", site, "migrate")
		cmd.Dir = root
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err = cmd.Start()
		if err == nil {
			fault.Hit("KP-EXIT-1")
			err = cmd.Wait()
		}
		outcome := "ok"
		if err != nil {
			outcome = "failed"
		}
		if e := record("migrate", site, outcome); e != nil {
			return rollback(e)
		}
		if err != nil {
			return rollback(fmt.Errorf("migrate site %s failed: %w", site, err))
		}
	}
	if err = record("finalized", "", "ok"); err != nil {
		return rollback(err)
	}
	return nil
}

// SyncMaintenanceArchiveState finalizes the archive shape at an explicit root.
func SyncMaintenanceArchiveState(root, directory, pkg string) error {
	return syncAppStateAt(root, directory, pkg)
}
