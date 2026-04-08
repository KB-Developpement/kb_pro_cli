package bench

import (
	"fmt"
	"os/exec"
	"strings"
)

// GetApp runs "bench get-app <url>" inside the bench container.
// It returns combined stdout+stderr on error for diagnostic purposes.
func GetApp(url string) error {
	out, err := runBench("get-app", url)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// InstallApp runs "bench --site <site> install-app <appName> --force".
func InstallApp(site, appName string) error {
	out, err := runBench("--site", site, "install-app", appName, "--force")
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// runBench executes a bench command from the bench root and returns combined output.
func runBench(args ...string) (string, error) {
	cmd := exec.Command("bench", args...)
	cmd.Dir = benchRoot
	raw, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(raw))
	return out, err
}
