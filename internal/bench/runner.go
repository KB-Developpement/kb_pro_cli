package bench

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// GetApp runs "bench get-app <url> [--branch <name>]" inside the bench container.
// If branch is non-empty after trimming, "--branch" is passed to bench get-app.
// If token is non-empty, credentials are supplied via a temporary git-credentials
// file (mode 0600) to avoid exposing the token in process arguments.
// Returns combined stdout+stderr and any error.
func GetApp(ctx context.Context, rawURL, token, branch string) (string, error) {
	cloneURL := rawURL
	var extraEnv []string
	var cleanup func()

	if token != "" {
		credsFile, err := writeTempCredentials(rawURL, token)
		if err == nil {
			cleanup = func() { os.Remove(credsFile) }
			extraEnv = []string{
				"GIT_CONFIG_COUNT=1",
				"GIT_CONFIG_KEY_0=credential.helper",
				fmt.Sprintf("GIT_CONFIG_VALUE_0=store --file=%s", credsFile),
			}
		} else {
			// Fallback: embed token in URL when temp-file creation fails.
			if u, parseErr := url.Parse(rawURL); parseErr == nil {
				u.User = url.UserPassword("x-access-token", token)
				cloneURL = u.String()
			}
		}
	}
	if cleanup != nil {
		defer cleanup()
	}

	args := []string{"get-app", cloneURL}
	if b := strings.TrimSpace(branch); b != "" {
		args = append(args, "--branch", b)
	}
	return runBenchWithEnv(ctx, extraEnv, args...)
}

// InstallApp runs "bench --site <site> install-app <appName> --force".
func InstallApp(ctx context.Context, site, appName string) (string, error) {
	return runBench(ctx, "--site", site, "install-app", appName, "--force")
}

// UninstallApp runs "bench --site <site> uninstall-app <appName> -y [--force]".
// -y bypasses bench's interactive confirmation (the UI already confirmed).
func UninstallApp(ctx context.Context, site, appName string, force bool) (string, error) {
	args := []string{"--site", site, "uninstall-app", appName, "-y"}
	if force {
		args = append(args, "--force")
	}
	return runBench(ctx, args...)
}

// RemoveApp runs "bench remove-app <appName>".
// This deletes the app source from the bench apps folder entirely.
func RemoveApp(ctx context.Context, appName string) (string, error) {
	return runBench(ctx, "remove-app", appName)
}

// runBench executes a bench command from the bench root and returns combined output.
func runBench(ctx context.Context, args ...string) (string, error) {
	return runBenchWithEnv(ctx, nil, args...)
}

// runBenchWithEnv executes a bench command with optional extra environment variables
// appended to the current process environment.
func runBenchWithEnv(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "bench", args...)
	cmd.Dir = benchRoot
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	raw, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(raw)), err
}

// writeTempCredentials writes a git-credentials file (mode 0600) containing
// an HTTPS credential line for the given URL and token.
// The caller is responsible for removing the file when done.
//
// git-credentials format: https://user:password@host
func writeTempCredentials(rawURL, token string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	// os.CreateTemp creates the file with mode 0600 on Unix.
	f, err := os.CreateTemp("", "kb-git-creds-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, writeErr := fmt.Fprintf(f, "%s://x-access-token:%s@%s\n", u.Scheme, token, u.Host)
	f.Close()
	if writeErr != nil {
		os.Remove(name)
		return "", writeErr
	}
	return name, nil
}
