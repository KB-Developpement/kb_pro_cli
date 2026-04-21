package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// defaultBenchDir is the bench path inside the standard KB dev container image.
const defaultBenchDir = "/workspace/frappe-bench"

// benchDir returns the Frappe bench directory used for `bench` CLI calls and app paths.
// Set KB_BENCH_ROOT when your bench lives elsewhere (e.g. ~/frappe-bench on a host machine).
func benchDir() string {
	if v := strings.TrimSpace(os.Getenv("KB_BENCH_ROOT")); v != "" {
		return v
	}
	return defaultBenchDir
}

// InBenchContainer returns true if the current environment looks like a Frappe bench container.
func InBenchContainer() bool {
	info, err := os.Stat(benchDir() + "/apps")
	return err == nil && info.IsDir()
}

// DetectSiteName attempts to determine the active Frappe site name.
// It first reads sites/currentsite.txt, then falls back to listing site directories.
func DetectSiteName() (string, error) {
	root := benchDir()

	// Primary: currentsite.txt
	data, err := os.ReadFile(root + "/sites/currentsite.txt")
	if err == nil {
		site := strings.TrimSpace(string(data))
		if site != "" {
			return site, nil
		}
	}

	// Fallback: list directories under sites/, exclude "assets"
	entries, err := os.ReadDir(root + "/sites")
	if err != nil {
		return "", fmt.Errorf("cannot read sites directory: %w", err)
	}

	var sites []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != "assets" {
			sites = append(sites, e.Name())
		}
	}

	switch len(sites) {
	case 0:
		return "", fmt.Errorf("no sites found in %s/sites", root)
	case 1:
		return sites[0], nil
	default:
		return "", fmt.Errorf("multiple sites found (%s); set the active site with: bench use <site>",
			strings.Join(sites, ", "))
	}
}

// DetectAppsInBench returns a set of app names whose source folder exists under bench/apps/.
// This reflects what has been downloaded via bench get-app, regardless of site installation.
func DetectAppsInBench() map[string]bool {
	entries, err := os.ReadDir(benchDir() + "/apps")
	if err != nil {
		return map[string]bool{}
	}
	result := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			result[e.Name()] = true
		}
	}
	return result
}

// DetectFrappeOrigin checks the git remote of apps/frappe to determine whether
// it is the stock Frappe repo (frappe/frappe) or the KB fork (KB-Developpement/kb_frappe).
// Returns (true, nil) for stock Frappe, (false, nil) for the KB fork.
// Returns a non-nil error when the directory is absent or has an unrecognised remote.
func DetectFrappeOrigin() (isStock bool, err error) {
	frappeDir := filepath.Join(benchDir(), "apps", "frappe")
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
	cmd.Dir = frappeDir
	out, runErr := cmd.Output()
	if runErr != nil {
		return false, fmt.Errorf("could not read frappe git remote: %w", runErr)
	}
	remote := strings.TrimSpace(string(out))
	switch {
	case strings.Contains(remote, "KB-Developpement/kb_frappe"):
		return false, nil
	case strings.Contains(remote, "frappe/frappe"):
		return true, nil
	default:
		return false, fmt.Errorf("unrecognised frappe remote %q — cannot determine Frappe origin", remote)
	}
}

// DetectInstalledApps returns a set of app names currently installed on the given site.
// On failure it returns nil and the error — callers should treat nil as "unknown".
func DetectInstalledApps(site string) (map[string]bool, error) {
	// --format json outputs {"site_name": ["app1", "app2", ...]} with clean app names only.
	// The default text format includes version and branch per line which breaks name matching.
	cmd := exec.Command("bench", "--site", site, "list-apps", "--format", "json")
	cmd.Dir = benchDir()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bench list-apps: %w", err)
	}

	var result map[string][]string
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parsing bench list-apps output: %w", err)
	}

	installed := map[string]bool{}
	for _, appList := range result {
		for _, app := range appList {
			app = strings.TrimSpace(app)
			if app != "" {
				installed[app] = true
			}
		}
	}
	return installed, nil
}
