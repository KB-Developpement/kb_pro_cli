package bench

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const benchRoot = "/workspace/frappe-bench"

// InBenchContainer returns true if the current environment looks like a Frappe bench container.
func InBenchContainer() bool {
	info, err := os.Stat(benchRoot + "/apps")
	return err == nil && info.IsDir()
}

// DetectSiteName attempts to determine the active Frappe site name.
// It first reads sites/currentsite.txt, then falls back to listing site directories.
func DetectSiteName() (string, error) {
	// Primary: currentsite.txt
	data, err := os.ReadFile(benchRoot + "/sites/currentsite.txt")
	if err == nil {
		site := strings.TrimSpace(string(data))
		if site != "" {
			return site, nil
		}
	}

	// Fallback: list directories under sites/, exclude "assets"
	entries, err := os.ReadDir(benchRoot + "/sites")
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
		return "", fmt.Errorf("no sites found in %s/sites", benchRoot)
	case 1:
		return sites[0], nil
	default:
		return "", fmt.Errorf("multiple sites found (%s); set the active site with: bench use <site>",
			strings.Join(sites, ", "))
	}
}

// DetectInstalledApps returns a set of app names currently installed on the given site.
// On failure it returns nil and the error — callers should treat nil as "unknown".
func DetectInstalledApps(site string) (map[string]bool, error) {
	cmd := exec.Command("bench", "--site", site, "list-apps")
	cmd.Dir = benchRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bench list-apps: %w", err)
	}

	installed := map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && line != site {
			installed[line] = true
		}
	}
	return installed, nil
}
