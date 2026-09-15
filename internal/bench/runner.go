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

	if err := extractTarGzStripped(archivePath, stagingDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", fmt.Errorf("extract archive: %w", err)
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
func SyncAppState(appName string) error {
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
		Version:    readAppVersion(root, appName),
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

// UpdateFromArchive upgrades an existing KB app from a .tar.gz source archive.
//
// Steps: atomically replace app directory → bench setup requirements (python +
// node) → pip install -e → bench build → bench migrate. The directory
// replacement uses a .kb-new sibling on the same filesystem so os.Rename is a
// cheap atomic syscall, and the previous source is kept as a .kb-old sibling
// until the upgrade succeeds.
//
// Recovery: if any step before `bench migrate` fails, the new directory is
// removed, the previous source is renamed back into place, pip install -e is
// re-run on it (best effort) and the returned error says the previous version
// was restored. If `bench migrate` fails the swap is NOT rolled back (the
// schema may be half-applied); .kb-old is kept and its path is included in the
// error so an operator can recover manually.
//
// The caller is responsible for removing archivePath after this returns.
func UpdateFromArchive(ctx context.Context, archivePath, appName string) (string, error) {
	appDir := filepath.Join(benchDir(), "apps", appName)
	stagingDir := appDir + ".kb-new"

	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}

	if err := extractTarGzStripped(archivePath, stagingDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", fmt.Errorf("extract archive: %w", err)
	}

	return swapAppDir(appDir, stagingDir, upgradeSteps{
		SetupRequirements: func() (string, error) { return setupRequirementsPythonAndNode(ctx, appName) },
		PipInstall:        func() (string, error) { return PipInstallEditable(ctx, appName) },
		Build:             func() (string, error) { return BuildApp(ctx, appName) },
		Migrate:           func() (string, error) { return runBench(ctx, "migrate") },
	})
}

// upgradeSteps are the post-swap bench operations. They are injectable so the
// directory swap and its rollback can be tested without a real bench.
type upgradeSteps struct {
	SetupRequirements func() (string, error)
	PipInstall        func() (string, error)
	Build             func() (string, error)
	Migrate           func() (string, error)
}

// swapAppDir moves stagingDir into appDir (keeping the previous source as
// appDir+".kb-old"), runs the post-swap steps, and rolls back to the previous
// source if any step before Migrate fails. See UpdateFromArchive for the
// recovery contract.
func swapAppDir(appDir, stagingDir string, steps upgradeSteps) (string, error) {
	oldDir := appDir + ".kb-old"
	if err := os.RemoveAll(oldDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", fmt.Errorf("remove stale backup %s: %w", oldDir, err)
	}

	hadOld := false
	if _, err := os.Stat(appDir); err == nil {
		if err := os.Rename(appDir, oldDir); err != nil {
			_ = os.RemoveAll(stagingDir)
			return "", fmt.Errorf("back up old app dir: %w", err)
		}
		hadOld = true
	}

	// restore puts the previous source back and re-registers it (best effort).
	restore := func(cause error, stage string) error {
		if !hadOld {
			return fmt.Errorf("%s: %w", stage, cause)
		}
		_ = os.RemoveAll(appDir)
		if err := os.Rename(oldDir, appDir); err != nil {
			return fmt.Errorf("%s: %w (ROLLBACK FAILED — previous version left at %s: %v)", stage, cause, oldDir, err)
		}
		if steps.PipInstall != nil {
			_, _ = steps.PipInstall()
		}
		return fmt.Errorf("%s: %w (the previous version was restored)", stage, cause)
	}

	if err := os.Rename(stagingDir, appDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", restore(err, "replace app dir")
	}

	reqOut, err := steps.SetupRequirements()
	if err != nil {
		return reqOut, restore(err, "setup requirements")
	}

	if _, pipErr := steps.PipInstall(); pipErr != nil {
		return reqOut, restore(pipErr, "pip install -e")
	}

	buildOut, err := steps.Build()
	if err != nil {
		return combineBenchOutput(reqOut, buildOut), restore(err, "build assets")
	}

	migrateOut, migrateErr := steps.Migrate()
	out := combineBenchOutput(reqOut, combineBenchOutput(buildOut, migrateOut))
	if migrateErr != nil {
		// Do not roll back: the schema may be half-migrated.
		if hadOld {
			return out, fmt.Errorf("bench migrate: %w (app files were upgraded and NOT rolled back; the previous source is kept at %s)", migrateErr, oldDir)
		}
		return out, fmt.Errorf("bench migrate: %w", migrateErr)
	}

	_ = os.RemoveAll(oldDir)
	return out, nil
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
	return fsutil.WriteFileAtomic(path, []byte(b.String()), 0o644)
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
	_ = fsutil.WriteFileAtomic(path, []byte(result), 0o644)
}

// readAppVersion reads the version string from <app>/<app>/__version__.py,
// falling back to __version__ in the package <app>/<app>/__init__.py (where
// frappe itself and most KB apps declare it), then to app_version in
// <app>/<app>/hooks.py. Returns "" when no candidate declares a version.
func readAppVersion(benchRoot, appName string) string {
	candidates := []struct {
		file   string
		prefix string
	}{
		{filepath.Join(benchRoot, "apps", appName, appName, "__version__.py"), "__version__"},
		{filepath.Join(benchRoot, "apps", appName, appName, "__init__.py"), "__version__"},
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
