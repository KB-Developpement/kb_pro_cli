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
// It reads sites/common_site_config.json (default_site), then legacy currentsite.txt,
// then falls back to listing site directories.
func DetectSiteName() (string, error) {
	root := benchDir()

	if site, err := defaultSiteFromCommonConfig(filepath.Join(root, "sites", "common_site_config.json")); err == nil && site != "" {
		return site, nil
	}

	// Legacy: currentsite.txt
	data, err := os.ReadFile(root + "/sites/currentsite.txt")
	if err == nil {
		site := strings.TrimSpace(string(data))
		if site != "" {
			return site, nil
		}
	}

	// Fallback: list site directories (must contain site_config.json).
	sites, err := listFrappeSiteDirs(filepath.Join(root, "sites"))
	if err != nil {
		return "", err
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

func defaultSiteFromCommonConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var cfg struct {
		DefaultSite string `json:"default_site"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", err
	}
	return strings.TrimSpace(cfg.DefaultSite), nil
}

// listFrappeSiteDirs returns site folder names under sitesRoot that contain site_config.json.
// Non-site directories (assets, __pycache__, etc.) are ignored.
func listFrappeSiteDirs(sitesRoot string) ([]string, error) {
	entries, err := os.ReadDir(sitesRoot)
	if err != nil {
		return nil, fmt.Errorf("cannot read sites directory: %w", err)
	}

	var sites []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cfg := filepath.Join(sitesRoot, e.Name(), "site_config.json")
		info, statErr := os.Stat(cfg)
		if statErr == nil && !info.IsDir() {
			sites = append(sites, e.Name())
		}
	}
	return sites, nil
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

// DetectFrappeOrigin checks the git remotes of apps/frappe to determine whether
// it is the stock Frappe repo (frappe/frappe) or the KB fork (KB-Developpement/kb_frappe).
// Returns (true, nil) for stock Frappe, (false, nil) for the KB fork.
// Returns a non-nil error when the directory is absent or has no recognisable remote.
// All remote names are checked (origin, upstream, etc.) to handle different bench setups.
func DetectFrappeOrigin() (isStock bool, err error) {
	frappeDir := filepath.Join(benchDir(), "apps", "frappe")
	cmd := exec.Command("git", "remote", "-v")
	cmd.Dir = frappeDir
	out, runErr := cmd.Output()
	if runErr != nil {
		return false, fmt.Errorf("could not read frappe git remotes: %w", runErr)
	}
	remotes := string(out)
	switch {
	case strings.Contains(remotes, "KB-Developpement/kb_frappe"):
		return false, nil
	case strings.Contains(remotes, "frappe/frappe"):
		return true, nil
	default:
		return false, fmt.Errorf("unrecognised frappe remotes — cannot determine Frappe origin")
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
