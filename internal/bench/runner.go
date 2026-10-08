package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
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

// PipInstallEditable registers the app as an editable Python package in the bench venv,
// mirroring bench's own install_app() logic (uv preferred, pip fallback).
func PipInstallEditable(ctx context.Context, appDirName string) (string, error) {
	root := benchDir()
	appPath := filepath.Join(root, "apps", appDirName)
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

// appStateEntry mirrors the schema bench writes in sites/apps.json
// (bench/bench.py BenchApps.update_apps_states). Archive-installed apps have
// no git history, so is_repo is always false and resolution is "not a repo".
type appStateEntry struct {
	IsRepo     bool     `json:"is_repo"`
	Resolution string   `json:"resolution"`
	Required   []string `json:"required"`
	Idx        int      `json:"idx"`
	Version    string   `json:"version"`
}

// SyncAppState writes the app's entry into sites/apps.json using the same
// schema that bench uses. It reads the file with json.RawMessage so that
// existing entries (which may contain nested objects, ints, or bools) are
// preserved exactly — a typed struct unmarshal would fail on mixed types and
// silently wipe the whole file on the next write.
func SyncAppState(dir, appName string) error {
	root := benchDir()
	path := filepath.Join(root, "sites", "apps.json")

	// Preserve all existing entries verbatim regardless of their Go types.
	// If the file exists but is not valid JSON, bail out rather than writing a
	// file that silently drops every other app's state.
	state := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if jsonErr := json.Unmarshal(raw, &state); jsonErr != nil {
			return fmt.Errorf("apps.json is not valid JSON — refusing to overwrite: %w", jsonErr)
		}
	}

	// Keep the existing idx when re-syncing an already-tracked app.
	idx := len(state) + 1
	if existing, ok := state[appName]; ok {
		var prev struct {
			Idx int `json:"idx"`
		}
		if json.Unmarshal(existing, &prev) == nil && prev.Idx > 0 {
			idx = prev.Idx
		}
	}

	entry := appStateEntry{
		IsRepo:     false,
		Resolution: "not a repo",
		Required:   []string{},
		Idx:        idx,
		Version:    readAppVersion(root, dir, appName),
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	state[appName] = entryJSON

	out, err := json.MarshalIndent(state, "", "\t")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, out, 0o644)
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

// appendAppToAppsTxt adds appName as a line to sites/apps.txt if not already
// listed. added reports whether this call wrote the line.
func appendAppToAppsTxt(benchRoot, appName string) (added bool, err error) {
	path := filepath.Join(benchRoot, "sites", "apps.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w (expected a Frappe bench with sites/apps.txt)", path, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == appName {
			return false, nil
		}
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(content, "\n"))
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(appName)
	b.WriteByte('\n')
	if err := fsutil.WriteFileAtomic(path, []byte(b.String()), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// removeAppFromAppsTxt removes appName from sites/apps.txt. A rollback depends
// on it, so a failure is returned.
func removeAppFromAppsTxt(benchRoot, appName string) error {
	path := filepath.Join(benchRoot, "sites", "apps.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
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
	return fsutil.WriteFileAtomic(path, []byte(result), 0o644)
}

// ReadAppVersion is readAppVersion for callers outside the package.
func ReadAppVersion(benchRoot, dir, pkg string) string { return readAppVersion(benchRoot, dir, pkg) }

// readAppVersion reads the version string from apps/<dir>/<pkg>/__version__.py,
// falling back to __version__ in the package apps/<dir>/<pkg>/__init__.py (where
// frappe itself and most KB apps declare it), then to app_version in
// apps/<dir>/<pkg>/hooks.py. Returns "" when no candidate declares a version.
func readAppVersion(benchRoot, dir, pkg string) string {
	candidates := []struct {
		file   string
		prefix string
	}{
		{filepath.Join(benchRoot, "apps", dir, pkg, "__version__.py"), "__version__"},
		{filepath.Join(benchRoot, "apps", dir, pkg, "__init__.py"), "__version__"},
		{filepath.Join(benchRoot, "apps", dir, pkg, "hooks.py"), "app_version"},
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

// honchoPattern matches only the honcho process itself ("…/honcho start" at
// the end of the command line), never a shell or editor whose command line
// merely mentions it.
const honchoPattern = `(^|/)honcho start$`

// IsDevServerRunning reports whether the bench dev server (honcho) is running.
func IsDevServerRunning() bool {
	return exec.Command("pgrep", "-f", honchoPattern).Run() == nil
}

// IsProdWebServerRunning reports whether a production web server (gunicorn) is
// running without honcho — i.e. the bench is in prod mode.
func IsProdWebServerRunning() bool {
	if IsDevServerRunning() {
		return false
	}
	return exec.Command("pgrep", "-f", "gunicorn").Run() == nil
}

// RestartDevServerIfRunning restarts bench start (honcho) if it is currently
// running. Call after app install/upgrade so the dev server picks up new Python
// packages, DocTypes, and schema changes from the running process.
// Returns (false, nil) when the server is not running (prod bench, manually
// stopped) — safe to call unconditionally. Use StartDevServer when the server
// was running before the operation but is gone now: there is nothing left to
// pkill and this would report "not running" and do nothing.
func RestartDevServerIfRunning(ctx context.Context) (bool, error) {
	if err := exec.CommandContext(ctx, "pgrep", "-f", honchoPattern).Run(); err != nil {
		return false, nil // not running — no-op
	}
	_ = exec.CommandContext(ctx, "pkill", "-f", honchoPattern).Run()
	time.Sleep(time.Second)
	if err := StartDevServer(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// StartDevServer launches a detached `bench start` from the bench root with its
// output appended to logs/bench-start.log. It does not check whether a dev
// server is already running — callers must.
//
// ctx is accepted for symmetry with the other bench helpers but is deliberately
// not attached to the process: the dev server has to outlive the kb invocation
// that started it, so cancelling ctx (or kb exiting) must not kill it.
func StartDevServer(ctx context.Context) error {
	_ = ctx
	root := benchDir()

	logPath := benchStartLogPath(root)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("create log dir for bench start: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := benchStartCmd(root)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start bench start: %w", err)
	}
	// Detached: we never wait on it, so release the process handle.
	_ = cmd.Process.Release()
	return nil
}

// benchStartLogPath is where a detached `bench start` writes its output.
func benchStartLogPath(benchRoot string) string {
	return filepath.Join(benchRoot, "logs", "bench-start.log")
}

// benchStartCmd builds the detached `bench start` command. It execs bench
// directly — no shell — so a bench root containing spaces or shell
// metacharacters (KB_BENCH_ROOT is attacker-influencable configuration) can
// never be interpreted as code. Factored out so it can be asserted in tests
// without running anything.
func benchStartCmd(benchRoot string) *exec.Cmd {
	cmd := exec.Command("bench", "start")
	cmd.Dir = benchRoot
	cmd.SysProcAttr = detachedSysProcAttr()
	return cmd
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
