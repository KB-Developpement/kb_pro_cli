package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

func TestInstallFailureShowsBenchOutput(t *testing.T) {
	h := newBenchH(t)
	t.Setenv("KB_FAKE_FAIL", "setup requirements --python")
	out, err := h.exec("install", "--apps", "kb_pro")
	if err == nil {
		t.Fatal("requirements failure was ignored")
	}
	if !strings.Contains(out, "fake bench: injected failure") {
		t.Fatalf("requirements diagnostics missing from output:\n%s", out)
	}
}

func TestInstallLegacyDistriArchiveRefusedBeforeRegistration(t *testing.T) {
	h := newBenchH(t)
	h.srv.LineTag = "v0.5.0"
	h.srv.Files["kb_distri@v0.5.0"] = []testutil.File{
		{Name: "pyproject.toml", Body: "[project]\nname = 'zephyr_distri'\n"},
		{Name: "zephyr_distri/__init__.py", Body: "__version__ = '1.0.1'\n"},
	}
	out, err := h.exec("install", "--apps", "kb_distri")
	if err == nil || !strings.Contains(out, "zephyr_distri") {
		t.Fatalf("expected archive identity refusal: %v\n%s", err, out)
	}
	for _, line := range h.benchLogLines() {
		if strings.Contains(line, "setup requirements") || strings.Contains(line, "install-app kb_distri") {
			t.Fatalf("registration ran for a mismatched archive: %s", line)
		}
	}
	for _, name := range []string{"kb_distri", "zephyr_distri"} {
		if _, err := os.Stat(filepath.Join(h.root, "apps", name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected source directory %s: %v", name, err)
		}
	}
}
