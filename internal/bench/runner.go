package bench

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// GetApp runs "bench get-app <url>" inside the bench container.
// If token is non-empty it is embedded in the URL as https://<token>@host/...
// so that private repos clone without an interactive credential prompt.
// It returns combined stdout+stderr on error for diagnostic purposes.
func GetApp(rawURL, token string) error {
	cloneURL := rawURL
	if token != "" {
		if u, err := url.Parse(rawURL); err == nil {
			u.User = url.User(token)
			cloneURL = u.String()
		}
	}
	out, err := runBench("get-app", cloneURL)
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

// UninstallApp runs "bench --site <site> uninstall-app <appName> -y [--force]".
// -y bypasses bench's interactive confirmation (we already confirmed in the UI).
// force adds --force to override any remaining guards.
func UninstallApp(site, appName string, force bool) error {
	args := []string{"--site", site, "uninstall-app", appName, "-y"}
	if force {
		args = append(args, "--force")
	}
	out, err := runBench(args...)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// RemoveApp runs "bench remove-app <appName>".
// This deletes the app source from the bench apps folder entirely.
func RemoveApp(appName string) error {
	out, err := runBench("remove-app", appName)
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
