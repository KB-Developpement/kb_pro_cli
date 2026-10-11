package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

func TestMismatchedArchiveRefusedBeforeBenchAndSwap(t *testing.T) {
	for _, command := range []string{CmdAdd, CmdInstall, CmdUpgrade, CmdInitFrappe} {
		t.Run(command, func(t *testing.T) {
			f := newFixture(t)
			if command == CmdUpgrade || command == CmdInitFrappe {
				f.writeApp("kb_distri", map[string]string{"keep.txt": "previous source"})
			}
			dl := f.download(map[string]string{
				"pyproject.toml":            "[project]\nname = 'zephyr_distri'\n",
				"zephyr_distri/__init__.py": "__version__ = '1.0.1'\n",
			})
			c := &calls{}
			_, err := applyWithSteps(f.root, ApplyPlan{Command: command,
				App: apps.App{Name: "kb_distri", Package: "kb_distri", Directory: "kb_distri", Line: 1}, Download: dl}, okSteps(c))
			if err == nil || !strings.Contains(err.Error(), "zephyr_distri") {
				t.Fatalf("error = %v", err)
			}
			if len(c.names) != 0 {
				t.Fatalf("Bench ran before identity validation: %v", c.names)
			}
			if command == CmdUpgrade || command == CmdInitFrappe {
				if got := string(testutil.MustRead(t, filepath.Join(f.appDir("kb_distri"), "keep.txt"))); got != "previous source" {
					t.Fatalf("previous source changed: %q", got)
				}
			} else if _, err := os.Stat(f.appDir("kb_distri")); !os.IsNotExist(err) {
				t.Fatalf("live source was created: %v", err)
			}
			if f.journal().Pending() {
				t.Fatal("refused archive left a pending transaction")
			}
		})
	}
}

func TestArchiveProjectIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		wantError      bool
	}{
		{"matching", "[project]\nname = 'kb_distri'\n", false},
		{"multiline", "[project]\nname = '''kb_distri''' # comment\n", false},
		{"wrong name", "[project]\nname = 'zephyr_distri'\n", true},
		{"missing name", "[project]\nversion = '1.0.0'\n", true},
		{"malformed", "[project\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.MkdirAll(filepath.Join(root, "kb_distri"), 0o755)
			os.WriteFile(filepath.Join(root, "kb_distri", "__init__.py"), nil, 0o644)
			os.WriteFile(filepath.Join(root, "pyproject.toml"), []byte(tc.metadata), 0o644)
			if err := validateArchiveIdentity(root, "kb_distri", "kb_distri"); (err != nil) != tc.wantError {
				t.Fatalf("error = %v; wantError %v", err, tc.wantError)
			}
		})
	}
}
