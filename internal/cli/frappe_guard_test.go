package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const (
	stockFrappeRemotes = "origin  https://github.com/frappe/frappe.git (fetch)\norigin  https://github.com/frappe/frappe.git (push)"
	kbFrappeRemotes    = "origin  https://github.com/KB-Developpement/kb_frappe.git (fetch)\norigin  https://github.com/KB-Developpement/kb_frappe.git (push)"
	unknownRemotes     = "origin  https://git.example.com/some/fork.git (fetch)"
)

// appCommandCtors are the subcommands that mutate the bench and therefore need
// the stock-Frappe guard.
var appCommandCtors = map[string]func() *cobra.Command{
	"add":          newAddCmd,
	"site-install": newSiteInstallCmd,
	"install":      newInstallCmd,
	"upgrade":      newUpgradeCmd,
}

// fakeGitOnPath installs a `git` script as the only executable on PATH. It
// prints remotes on stdout and exits with exitCode, so DetectFrappeOrigin can
// be driven without a real repository. Only shell builtins are used: nothing
// else is left on PATH.
func fakeGitOnPath(t *testing.T, remotes string, exitCode int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake git script requires a POSIX shell")
	}
	binDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\necho '%s'\nexit %d\n", remotes, exitCode)
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
}

// benchWithFrappeDir creates a bench root containing apps/frappe — the working
// directory `git remote -v` runs from — and points KB_BENCH_ROOT at it.
func benchWithFrappeDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "frappe"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KB_BENCH_ROOT", root)
	return root
}

// noInput forces --no-input for the duration of the test so nothing can prompt.
func noInput(t *testing.T) {
	t.Helper()
	orig := globalFlags.NoInput
	globalFlags.NoInput = true
	t.Cleanup(func() { globalFlags.NoInput = orig })
}

// TestRequireKBFrappe pins the guard: only a POSITIVE stock match blocks.
// A detection error (missing, unreadable or unrecognised remotes) must stay
// usable — the same rule the interactive menu follows (ADR-004).
func TestRequireKBFrappe(t *testing.T) {
	tests := []struct {
		name        string
		remotes     string
		exitCode    int
		skip        bool
		wantRefusal bool
	}{
		{name: "stock frappe refuses", remotes: stockFrappeRemotes, wantRefusal: true},
		{name: "kb frappe fork is allowed", remotes: kbFrappeRemotes},
		{name: "git exits non-zero — must not block", remotes: "", exitCode: 1},
		{name: "unrecognised remotes — must not block", remotes: unknownRemotes},
		{name: "--skip-frappe-check overrides a positive stock match", remotes: stockFrappeRemotes, skip: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			benchWithFrappeDir(t)
			fakeGitOnPath(t, tc.remotes, tc.exitCode)

			err := requireKBFrappe(tc.skip)
			if tc.wantRefusal {
				if err == nil {
					t.Fatal("requireKBFrappe = nil, want a refusal on stock Frappe")
				}
				if !strings.Contains(err.Error(), "still stock Frappe") {
					t.Fatalf("requireKBFrappe error = %q, want it to name stock Frappe", err)
				}
				if !strings.Contains(err.Error(), "kb init-kb-frappe") {
					t.Fatalf("requireKBFrappe error = %q, want it to name the fix (kb init-kb-frappe)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("requireKBFrappe = %v, want nil", err)
			}
		})
	}
}

// TestAppCommandsRefuseStockFrappe reproduces the second defect: on a bench
// whose apps/frappe was still frappe/frappe, `kb install --no-input --apps
// kb_pro` downloaded and built kb_pro, then failed at bench install-app,
// leaving the app in the bench but not on the site. The menu already hides
// these actions on stock Frappe; the subcommands must refuse too, before any
// download or bench mutation.
func TestAppCommandsRefuseStockFrappe(t *testing.T) {
	for name, ctor := range appCommandCtors {
		t.Run(name, func(t *testing.T) {
			withDevNullStdout(t)
			benchWithFrappeDir(t)
			fakeGitOnPath(t, stockFrappeRemotes, 0)
			t.Setenv("HOME", t.TempDir())
			// Marks setup complete without a config.json; never contacted.
			t.Setenv("KB_LICENSE_SERVER", "http://127.0.0.1:9")
			noInput(t)

			cmd := ctor()
			cmd.SetArgs([]string{"--apps", "kb_pro"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("kb %s returned nil on stock Frappe — want a refusal", name)
			}
			if !strings.Contains(err.Error(), "still stock Frappe") {
				t.Fatalf("kb %s error = %q, want a refusal naming stock Frappe", name, err)
			}
		})
	}
}

// TestAppCommandsHaveSkipFrappeCheckFlag keeps the escape hatch on every
// guarded command.
func TestAppCommandsHaveSkipFrappeCheckFlag(t *testing.T) {
	for name, ctor := range appCommandCtors {
		if ctor().Flags().Lookup("skip-frappe-check") == nil {
			t.Errorf("kb %s has no --skip-frappe-check flag", name)
		}
	}
}
