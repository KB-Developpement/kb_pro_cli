package bench

import (
	"os"
	"path/filepath"
	"testing"
)

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
			if got := readAppVersion(root, "kb_test", "kb_test"); got != tc.want {
				t.Fatalf("readAppVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
