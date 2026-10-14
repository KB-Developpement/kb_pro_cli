package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func maintenanceBench(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "bench", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func CheckMaintenanceBench(ctx context.Context, root string) error {
	out, err := maintenanceBench(ctx, root, "--version")
	if err != nil {
		return fmt.Errorf("cannot determine Bench version: %w", err)
	}
	if out != "5.31.0" {
		return fmt.Errorf("maintenance requires captured Bench 5.31.0 schema; found %q", out)
	}
	return nil
}

func MaintenanceSites(root string) ([]string, error) {
	sites, err := listFrappeSiteDirs(filepath.Join(root, "sites"))
	if err != nil {
		return nil, err
	}
	if len(sites) == 0 {
		return nil, errors.New("maintenance requires at least one site")
	}
	return sites, nil
}

func maintenanceInstalledApps(ctx context.Context, root, site string) (map[string]bool, error) {
	if !maintenanceComponent(site) {
		return nil, errors.New("invalid site")
	}
	out, err := maintenanceBench(ctx, root, "--site", site, "list-apps", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("list installed apps for %s: %w", site, err)
	}
	var data map[string][]string
	if err = json.Unmarshal([]byte(out), &data); err != nil {
		return nil, fmt.Errorf("parse installed apps for %s: %w", site, err)
	}
	list, ok := data[site]
	if !ok || len(data) != 1 || len(list) == 0 {
		return nil, fmt.Errorf("list-apps did not return exactly site %s", site)
	}
	apps := map[string]bool{}
	for _, a := range list {
		if a == "" {
			return nil, errors.New("empty installed app name")
		}
		apps[a] = true
	}
	return apps, nil
}

func InstalledMaintenanceSites(ctx context.Context, root, pkg string) ([]string, error) {
	sites, err := MaintenanceSites(root)
	if err != nil {
		return nil, err
	}
	affected := []string{}
	for _, site := range sites {
		installed, err := maintenanceInstalledApps(ctx, root, site)
		if err != nil {
			return nil, err
		}
		if !installed["kb_pro"] {
			return nil, fmt.Errorf("site %s does not have kb_pro installed", site)
		}
		if installed[pkg] {
			affected = append(affected, site)
		}
	}
	if len(affected) == 0 {
		return nil, fmt.Errorf("app %s is not installed on any bench site", pkg)
	}
	return affected, nil
}

// MaintenanceBackup identifies one verified artifact, not command output.
// Each site produces three records: database, public files and private files.
type MaintenanceBackup struct{ Site, Path, TakenAt string }

var maintenanceBackupSafePath = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

func BackupMaintenanceSites(ctx context.Context, root string, sites []string) ([]MaintenanceBackup, error) {
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// Frappe embeds backup paths in its own shell commands without quoting.
	// Refuse paths that its native backup implementation cannot safely handle.
	if !maintenanceBackupSafePath.MatchString(root) {
		return nil, errors.New("native Frappe backup requires a root without whitespace or shell metacharacters")
	}
	for _, site := range sites {
		if !maintenanceComponent(site) || !maintenanceBackupSafePath.MatchString(site) {
			return nil, errors.New("native Frappe backup requires safe site names")
		}
	}
	if len(sites) == 0 {
		return nil, errors.New("no sites to back up")
	}
	parent := filepath.Join(root, ".kb", "maintenance", "backups")
	if err := maintenanceEnsureTree(root, parent); err != nil {
		return nil, err
	}
	result := []MaintenanceBackup{}
	seen := map[string]bool{}
	for _, site := range sites {
		if !maintenanceComponent(site) || seen[site] {
			return nil, errors.New("invalid or duplicate backup site")
		}
		seen[site] = true
		dir, err := os.MkdirTemp(parent, site+"-")
		if err != nil {
			return result, err
		}
		paths := []string{filepath.Join(dir, "database.sql.gz"), filepath.Join(dir, "public-files.tar"), filepath.Join(dir, "private-files.tar")}
		started := time.Now().UTC()
		_, err = maintenanceBench(ctx, root, "--site", site, "backup", "--with-files", "--backup-path-db", paths[0], "--backup-path-files", paths[1], "--backup-path-private-files", paths[2], "--backup-path-conf", filepath.Join(dir, "site-config.json"))
		if err != nil {
			return result, fmt.Errorf("backup site %s: %w", site, err)
		}
		for _, p := range paths {
			fi, err := os.Lstat(p)
			if err != nil {
				return result, fmt.Errorf("backup artifact missing for %s: %w", site, err)
			}
			if !fi.Mode().IsRegular() || fi.Size() <= 0 || fi.ModTime().Before(started.Add(-time.Second)) {
				return result, fmt.Errorf("backup artifact is not fresh, nonempty and regular: %s", p)
			}
			result = append(result, MaintenanceBackup{Site: site, Path: p, TakenAt: started.Format(time.RFC3339Nano)})
		}
		if err = syncMaintenanceTree(dir); err != nil {
			return result, err
		}
	}
	return result, nil
}

// PatchLogs reads only the applied patch names through Frappe's native execute.
func PatchLogs(ctx context.Context, root string, sites []string) (map[string][]string, error) {
	if len(sites) == 0 {
		return nil, errors.New("no sites for Patch Log snapshot")
	}
	result := map[string][]string{}
	for _, site := range sites {
		if !maintenanceComponent(site) {
			return nil, errors.New("invalid site")
		}
		if _, ok := result[site]; ok {
			return nil, errors.New("duplicate Patch Log site")
		}
		out, err := maintenanceBench(ctx, root, "--site", site, "execute", "frappe.get_all", "--args", `["Patch Log"]`, "--kwargs", `{"pluck":"patch","order_by":"patch","filters":{"skipped":0}}`)
		if err != nil {
			return nil, fmt.Errorf("read Patch Log for %s: %w", site, err)
		}
		patches := []string{}
		if out == "" {
			result[site] = patches
			continue
		} // Native execute prints only truthy results.
		if err = json.Unmarshal([]byte(out), &patches); err != nil || patches == nil {
			return nil, fmt.Errorf("Patch Log for %s did not return a JSON array", site)
		}
		for _, p := range patches {
			if p == "" {
				return nil, fmt.Errorf("invalid Patch Log entry for %s", site)
			}
		}
		// Patch Log uses random row identities, not a unique patch-name key.
		// Preserve repeated applied rows so a later rerun remains a visible delta.
		sort.Strings(patches)
		result[site] = patches
	}
	return result, nil
}
