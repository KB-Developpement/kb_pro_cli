package bench

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

// InstallApp runs "bench --site <site> install-app <appName>".
func InstallApp(ctx context.Context, site, appName string) (string, error) {
	return runBench(ctx, "--site", site, "install-app", appName)
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

// UpdateApp runs "bench update --apps <appName> --reset".
// --reset performs a git reset --hard to the upstream branch (required for shallow clones).
// This is a long-running operation: it pulls code, updates requirements, migrates all
// sites, rebuilds JS/CSS assets, and compiles translations. Run sequentially — never
// concurrently — as migrations and builds are bench-wide operations.
func UpdateApp(ctx context.Context, appName string) (string, error) {
	return runBench(ctx, "update", "--apps", appName, "--reset")
}

// GetAppFromArchive installs a new KB app from a .tar.gz source archive.
//
// GitHub release tarballs are plain source trees (no `.git`). `bench get-app`
// and plain `bench setup requirements <app>` construct bench.app.App, which
// calls git.Repo on the app path and crashes without `.git`. We use
// `bench setup requirements --python <app>` instead, which runs BenchSetup.python
// (pip/uv install -e apps/<app>) without that git metadata path.
//
// The caller is responsible for removing archivePath after this returns.
func GetAppFromArchive(ctx context.Context, archivePath, appName string) (string, error) {
	root := benchDir()
	if _, err := os.Stat(root); err != nil {
		return "", fmt.Errorf("bench directory %q is not accessible (set KB_BENCH_ROOT): %w", root, err)
	}

	appDir := filepath.Join(root, "apps", appName)
	if fi, err := os.Stat(appDir); err == nil && fi.IsDir() {
		return "", fmt.Errorf("app %q already exists at %s — remove it or use upgrade", appName, appDir)
	}

	stagingDir := appDir + ".kb-new"
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}

	tarCmd := exec.CommandContext(ctx, "tar", "-xzf", archivePath, "-C", stagingDir, "--strip-components=1")
	tarCmd.Dir = filepath.Dir(stagingDir)
	if out, err := tarCmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(stagingDir)
		return strings.TrimSpace(string(out)), fmt.Errorf("extract archive: %w", err)
	}

	if err := os.Rename(stagingDir, appDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", fmt.Errorf("move app into apps/: %w", err)
	}

	if err := appendAppToAppsTxt(root, appName); err != nil {
		_ = os.RemoveAll(appDir)
		return "", err
	}

	out, err := runBench(ctx, "setup", "requirements", "--python", appName)
	if err != nil && out != "" {
		return out, fmt.Errorf("%s: %w", out, err)
	}
	return out, err
}

// appendAppToAppsTxt adds appName as a line to sites/apps.txt if not already listed.
func appendAppToAppsTxt(benchRoot, appName string) error {
	path := filepath.Join(benchRoot, "sites", "apps.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w (expected a Frappe bench with sites/apps.txt)", path, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == appName {
			return nil
		}
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(content, "\n"))
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(appName)
	b.WriteByte('\n')
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// UpdateFromArchive upgrades an existing KB app from a .tar.gz source archive.
//
// It atomically replaces the app directory (using a .new sibling within the
// same filesystem so os.Rename is cheap) and then runs "bench migrate" to
// apply any schema changes.  Removing stale files from previous versions is
// handled by the full directory replacement rather than an in-place overlay.
//
// The caller is responsible for removing archivePath after this returns.
func UpdateFromArchive(ctx context.Context, archivePath, appName string) (string, error) {
	appDir := filepath.Join(benchDir(), "apps", appName)
	stagingDir := appDir + ".new"

	// Clean up any leftover staging dir from a previous failed upgrade.
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}

	tarCmd := exec.CommandContext(ctx, "tar", "-xzf", archivePath, "-C", stagingDir, "--strip-components=1")
	tarCmd.Dir = filepath.Dir(stagingDir)
	if out, err := tarCmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(stagingDir)
		return strings.TrimSpace(string(out)), fmt.Errorf("extract archive: %w", err)
	}

	// Atomically replace the app directory. Both paths are within the bench dir
	// (same filesystem), so os.Rename is an atomic rename(2) syscall.
	if err := os.RemoveAll(appDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", fmt.Errorf("remove old app dir: %w", err)
	}
	if err := os.Rename(stagingDir, appDir); err != nil {
		return "", fmt.Errorf("replace app dir: %w", err)
	}

	return runBench(ctx, "migrate", "--apps", appName)
}

// runBench executes a bench command from the bench root and returns combined output.
func runBench(ctx context.Context, args ...string) (string, error) {
	return runBenchWithEnv(ctx, nil, args...)
}

// runBenchWithEnv executes a bench command with optional extra environment variables
// appended to the current process environment.
func runBenchWithEnv(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	root := benchDir()
	if _, err := os.Stat(root); err != nil {
		return "", fmt.Errorf("bench directory %q is not accessible (set KB_BENCH_ROOT to your Frappe bench root): %w", root, err)
	}

	cmd := exec.CommandContext(ctx, "bench", args...)
	cmd.Dir = root
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
