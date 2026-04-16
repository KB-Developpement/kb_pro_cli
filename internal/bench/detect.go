package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
