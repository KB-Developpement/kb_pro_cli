package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

// GetAppFromArchive installs a new KB app from a .tar.gz source archive.
//
// GitHub release tarballs are plain source trees (no `.git`). `bench get-app`
// crashes on no-git paths, so we use `bench setup requirements` for deps and
// pip install -e for the editable package registration — matching what
// bench's own install_app() does after a git clone.
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

	reqOut, err := setupRequirementsPythonAndNode(ctx, appName)
	if err != nil {
		_ = os.RemoveAll(appDir)
		removeAppFromAppsTxt(root, appName)
		return "", fmt.Errorf("setup requirements for %s: %w", appName, err)
	}

	pipOut, err := PipInstallEditable(ctx, appName)
	if err != nil {
		_ = os.RemoveAll(appDir)
		removeAppFromAppsTxt(root, appName)
		return "", fmt.Errorf("pip install -e for %s: %w", appName, err)
	}

	return combineBenchOutput(reqOut, pipOut), nil
}

// PipInstallEditable registers the app as an editable Python package in the bench venv,
// mirroring bench's own install_app() logic (uv preferred, pip fallback).
func PipInstallEditable(ctx context.Context, appName string) (string, error) {
	root := benchDir()
	appPath := filepath.Join(root, "apps", appName)
	python := filepath.Join(root, "env", "bin", "python")

	// Try uv first (faster, no global lock).
	uvCmd := exec.CommandContext(ctx, python, "-m", "uv", "pip", "install", "--quiet", "--upgrade", "-e", appPath)
	uvCmd.Dir = root
	if out, err := uvCmd.CombinedOutput(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	cmd := exec.CommandContext(ctx, python, "-m", "pip", "install", "--quiet", "--upgrade", "-e", appPath)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// BuildApp runs "bench build --app <appName>" to compile JS/CSS assets.
func BuildApp(ctx context.Context, appName string) (string, error) {
	return runBench(ctx, "build", "--app", appName)
}

// SyncAppState writes the app's version into sites/apps.json.
// Branch and commit are left empty since archive-installed apps have no git history.
func SyncAppState(appName string) error {
	root := benchDir()
	path := filepath.Join(root, "sites", "apps.json")

	state := map[string]map[string]string{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &state)
	}

	state[appName] = map[string]string{
		"version": readAppVersion(root, appName),
		"branch":  "",
		"commit":  "",
	}

	out, err := json.MarshalIndent(state, "", "\t")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

// UpdateFromArchive upgrades an existing KB app from a .tar.gz source archive.
//
// It atomically replaces the app directory (using a .new sibling within the
// same filesystem so os.Rename is cheap) and then runs "bench migrate" to
// apply any schema changes. Removing stale files from previous versions is
// handled by the full directory replacement rather than an in-place overlay.
//
// The caller is responsible for removing archivePath after this returns.
func UpdateFromArchive(ctx context.Context, archivePath, appName string) (string, error) {
	appDir := filepath.Join(benchDir(), "apps", appName)
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

	if err := os.RemoveAll(appDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", fmt.Errorf("remove old app dir: %w", err)
	}
	if err := os.Rename(stagingDir, appDir); err != nil {
		return "", fmt.Errorf("replace app dir: %w", err)
	}

	reqOut, err := setupRequirementsPythonAndNode(ctx, appName)
	if err != nil {
		return reqOut, fmt.Errorf("setup requirements for %s: %w", appName, err)
	}

	if _, pipErr := PipInstallEditable(ctx, appName); pipErr != nil {
		return reqOut, fmt.Errorf("pip install -e for %s: %w", appName, pipErr)
	}

	migrateOut, err := runBench(ctx, "migrate")
	return combineBenchOutput(reqOut, migrateOut), err
}

// setupRequirementsPythonAndNode runs "bench setup requirements --python" then "--node".
func setupRequirementsPythonAndNode(ctx context.Context, appName string) (string, error) {
	outPy, err := runBench(ctx, "setup", "requirements", "--python", appName)
	if err != nil {
		return outPy, err
	}
	outNode, err := runBench(ctx, "setup", "requirements", "--node", appName)
	return combineBenchOutput(outPy, outNode), err
}

func combineBenchOutput(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n" + b
	}
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
	return os.WriteFile(path, []byte(b.String()), 0644)
}

// removeAppFromAppsTxt removes appName from sites/apps.txt (best-effort cleanup).
func removeAppFromAppsTxt(benchRoot, appName string) {
	path := filepath.Join(benchRoot, "sites", "apps.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) != appName {
			kept = append(kept, line)
		}
	}
	result := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if result != "" {
		result += "\n"
	}
	_ = os.WriteFile(path, []byte(result), 0644)
}

// readAppVersion reads the version string from <app>/<app>/__version__.py,
// falling back to app_version in <app>/<app>/hooks.py. Returns "" on failure.
func readAppVersion(benchRoot, appName string) string {
	candidates := []struct {
		file   string
		prefix string
	}{
		{filepath.Join(benchRoot, "apps", appName, appName, "__version__.py"), "__version__"},
		{filepath.Join(benchRoot, "apps", appName, appName, "hooks.py"), "app_version"},
	}
	for _, c := range candidates {
		data, err := os.ReadFile(c.file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			after, ok := strings.CutPrefix(line, c.prefix)
			if !ok {
				continue
			}
			after = strings.TrimSpace(after)
			if strings.HasPrefix(after, "=") {
				v := strings.TrimSpace(after[1:])
				return strings.Trim(v, `"'`)
			}
		}
	}
	return ""
}

// runBench executes a bench command from the bench root and returns combined output.
func runBench(ctx context.Context, args ...string) (string, error) {
	root := benchDir()
	if _, err := os.Stat(root); err != nil {
		return "", fmt.Errorf("bench directory %q is not accessible (set KB_BENCH_ROOT to your Frappe bench root): %w", root, err)
	}
	cmd := exec.CommandContext(ctx, "bench", args...)
	cmd.Dir = root
	raw, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(raw)), err
}
