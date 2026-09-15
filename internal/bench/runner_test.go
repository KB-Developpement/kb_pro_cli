package bench

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeTempBench builds a bench root with apps/<app> containing marker content
// and returns (root, path to a .tar.gz archive holding the new version).
func makeTempBench(t *testing.T, appName string) (root, archive string) {
	t.Helper()
	root = t.TempDir()
	appDir := filepath.Join(root, "apps", appName)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "marker.txt"), []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(t.TempDir(), appName)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "marker.txt"), []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive = filepath.Join(t.TempDir(), appName+".tar.gz")
	cmd := exec.Command("tar", "-czf", archive, "-C", filepath.Dir(src), appName)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar: %v: %s", err, out)
	}
	return root, archive
}

// pathWithTarOnly returns a PATH containing only a link to the real tar binary.
func pathWithTarOnly(t *testing.T) string {
	t.Helper()
	tarPath, err := exec.LookPath("tar")
	if err != nil {
		t.Skipf("tar not available: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(tarPath, filepath.Join(dir, "tar")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestUpdateFromArchive_RestoresOnSetupFailure proves that when a post-swap
// bench step fails, the previous app source is restored.
func TestUpdateFromArchive_RestoresOnSetupFailure(t *testing.T) {
	root, archive := makeTempBench(t, "kb_test")
	t.Setenv("KB_BENCH_ROOT", root)
	// PATH has tar (needed for extraction) but no `bench` → the first
	// post-swap step fails.
	t.Setenv("PATH", pathWithTarOnly(t))

	_, err := UpdateFromArchive(context.Background(), archive, "kb_test")
	if err == nil {
		t.Fatal("expected UpdateFromArchive to fail without bench")
	}

	data, readErr := os.ReadFile(filepath.Join(root, "apps", "kb_test", "marker.txt"))
	if readErr != nil {
		t.Fatalf("app dir unreadable after failed upgrade: %v", readErr)
	}
	if string(data) != "OLD" {
		t.Errorf("app content after failed upgrade: got %q, want OLD (previous version not restored)", data)
	}
	if _, err := os.Stat(filepath.Join(root, "apps", "kb_test.kb-old")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".kb-old should be gone after a rollback, stat err=%v", err)
	}
}

// newSwapDirs creates appDir (content "OLD") and stagingDir (content "NEW").
func newSwapDirs(t *testing.T) (appDir, stagingDir string) {
	t.Helper()
	root := t.TempDir()
	appDir = filepath.Join(root, "apps", "kb_test")
	stagingDir = appDir + ".kb-new"
	for dir, content := range map[string]string{appDir: "OLD", stagingDir: "NEW"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return appDir, stagingDir
}

func marker(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "marker.txt"))
	if err != nil {
		t.Fatalf("read marker in %s: %v", dir, err)
	}
	return string(data)
}

func TestSwapAppDir(t *testing.T) {
	okStep := func() (string, error) { return "ok", nil }
	boom := errors.New("boom")
	failStep := func() (string, error) { return "failed", boom }

	tests := []struct {
		name         string
		steps        func(pipCalls *int) upgradeSteps
		wantErr      bool
		wantMarker   string // expected content of appDir after the call
		wantOldKept  bool
		wantRestored bool // rollback happened → pip re-run on restored app
		wantPipCalls int
	}{
		{
			name: "success removes .kb-old",
			steps: func(pip *int) upgradeSteps {
				return upgradeSteps{okStep, func() (string, error) { *pip++; return "ok", nil }, okStep, okStep}
			},
			wantMarker: "NEW",
		},
		{
			name: "setup requirements failure restores previous version",
			steps: func(pip *int) upgradeSteps {
				return upgradeSteps{failStep, func() (string, error) { *pip++; return "ok", nil }, okStep, okStep}
			},
			wantErr: true, wantMarker: "OLD", wantRestored: true, wantPipCalls: 1,
		},
		{
			name: "build failure restores previous version",
			steps: func(pip *int) upgradeSteps {
				return upgradeSteps{okStep, func() (string, error) { *pip++; return "ok", nil }, failStep, okStep}
			},
			wantErr: true, wantMarker: "OLD", wantRestored: true, wantPipCalls: 2,
		},
		{
			name: "migrate failure keeps .kb-old and does not roll back",
			steps: func(pip *int) upgradeSteps {
				return upgradeSteps{okStep, func() (string, error) { *pip++; return "ok", nil }, okStep, failStep}
			},
			wantErr: true, wantMarker: "NEW", wantOldKept: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			appDir, stagingDir := newSwapDirs(t)
			pipCalls := 0
			_, err := swapAppDir(appDir, stagingDir, tc.steps(&pipCalls))
			if (err != nil) != tc.wantErr {
				t.Fatalf("swapAppDir err = %v, wantErr %v", err, tc.wantErr)
			}
			if got := marker(t, appDir); got != tc.wantMarker {
				t.Errorf("appDir content = %q, want %q", got, tc.wantMarker)
			}
			_, statErr := os.Stat(appDir + ".kb-old")
			if tc.wantOldKept {
				if statErr != nil {
					t.Errorf(".kb-old should be kept after migrate failure: %v", statErr)
				} else if got := marker(t, appDir+".kb-old"); got != "OLD" {
					t.Errorf(".kb-old content = %q, want OLD", got)
				}
				if !strings.Contains(err.Error(), appDir+".kb-old") {
					t.Errorf("migrate error should name the backup path, got: %v", err)
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf(".kb-old should not exist, stat err = %v", statErr)
			}
			if tc.wantRestored {
				if pipCalls != tc.wantPipCalls {
					t.Errorf("pip install calls = %d, want %d (restored app must be re-registered)", pipCalls, tc.wantPipCalls)
				}
				if !strings.Contains(err.Error(), "previous version was restored") {
					t.Errorf("error should mention the restore, got: %v", err)
				}
			}
			if _, err := os.Stat(stagingDir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("staging dir should be gone, stat err = %v", err)
			}
		})
	}
}

// writeAppFile creates <root>/apps/<app>/<app>/<name> with the given content.
func writeAppFile(t *testing.T, root, app, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "apps", app, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadAppVersion covers every supported version location, their precedence,
// and the "no version anywhere" case. Real apps (frappe itself, kb_pro) declare
// __version__ in the package __init__.py, which used to be missed entirely and
// wrote "version": "" into sites/apps.json.
func TestReadAppVersion(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "__version__.py",
			files: map[string]string{"__version__.py": "__version__ = \"1.2.3\"\n"},
			want:  "1.2.3",
		},
		{
			name:  "package __init__.py",
			files: map[string]string{"__init__.py": "import frappe\n\n__version__ = \"1.0.14\"\n"},
			want:  "1.0.14",
		},
		{
			name:  "hooks.py app_version",
			files: map[string]string{"hooks.py": "app_name = \"kb_test\"\napp_version = '2.0.0'\n"},
			want:  "2.0.0",
		},
		{
			name: "__version__.py wins over __init__.py",
			files: map[string]string{
				"__version__.py": "__version__ = \"3.0.0\"\n",
				"__init__.py":    "__version__ = \"1.0.14\"\n",
			},
			want: "3.0.0",
		},
		{
			name: "__init__.py wins over hooks.py",
			files: map[string]string{
				"__init__.py": "__version__ = \"1.0.14\"\n",
				"hooks.py":    "app_version = \"9.9.9\"\n",
			},
			want: "1.0.14",
		},
		{
			name:  "no version anywhere",
			files: map[string]string{"__init__.py": "import frappe\n"},
			want:  "",
		},
		{
			name:  "no app dir at all",
			files: nil,
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, content := range tc.files {
				writeAppFile(t, root, "kb_test", name, content)
			}
			if got := readAppVersion(root, "kb_test"); got != tc.want {
				t.Fatalf("readAppVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
