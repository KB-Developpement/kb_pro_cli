package adopt

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func tree(t *testing.T, files map[string]string, execs ...string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range execs {
		if err := os.Chmod(filepath.Join(root, e), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func manifest(t *testing.T, root string) Manifest {
	t.Helper()
	m, err := BuildManifest(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCompareFullMatchIgnoresModeBitsButNotTheExecBit(t *testing.T) {
	rel := tree(t, map[string]string{"a.py": "1", "sub/run.sh": "#!/bin/sh"}, "sub/run.sh")
	inst := tree(t, map[string]string{"a.py": "1", "sub/run.sh": "#!/bin/sh"}, "sub/run.sh")
	_ = os.Chmod(filepath.Join(inst, "a.py"), 0o600)       // umask difference
	_ = os.Chmod(filepath.Join(inst, "sub/run.sh"), 0o700) // still executable
	d := Compare(manifest(t, inst), manifest(t, rel), "pkg")
	if !d.Match() || d.Compared != 2 {
		t.Fatalf("diff = %+v", d)
	}
	_ = os.Chmod(filepath.Join(inst, "a.py"), 0o755)
	d = Compare(manifest(t, inst), manifest(t, rel), "pkg")
	if d.Match() || !reflect.DeepEqual(d.Changed, []string{"a.py"}) {
		t.Fatalf("exec bit change: %+v", d)
	}
}

func TestCompareReportsAddedRemovedChanged(t *testing.T) {
	rel := tree(t, map[string]string{"keep.py": "1", "edit.py": "old", "gone.py": "x"})
	inst := tree(t, map[string]string{"keep.py": "1", "edit.py": "new", "extra.py": "y"})
	d := Compare(manifest(t, inst), manifest(t, rel), "pkg")
	if !reflect.DeepEqual(d.Added, []string{"extra.py"}) || !reflect.DeepEqual(d.Removed, []string{"gone.py"}) || !reflect.DeepEqual(d.Changed, []string{"edit.py"}) {
		t.Fatalf("diff = %+v", d)
	}
}

func TestCompareSymlinksAndTypes(t *testing.T) {
	rel := tree(t, map[string]string{"t.txt": "x"})
	inst := tree(t, map[string]string{"t.txt": "x"})
	_ = os.Symlink("t.txt", filepath.Join(rel, "link"))
	_ = os.Symlink("t.txt", filepath.Join(inst, "link"))
	if d := Compare(manifest(t, inst), manifest(t, rel), "pkg"); !d.Match() {
		t.Fatalf("equal symlinks: %+v", d)
	}
	_ = os.Remove(filepath.Join(inst, "link"))
	_ = os.Symlink("elsewhere", filepath.Join(inst, "link"))
	if d := Compare(manifest(t, inst), manifest(t, rel), "pkg"); d.Match() || len(d.Changed) != 1 {
		t.Fatalf("different symlink target: %+v", d)
	}
	_ = os.Remove(filepath.Join(inst, "link"))
	_ = os.WriteFile(filepath.Join(inst, "link"), []byte("t.txt"), 0o644)
	if d := Compare(manifest(t, inst), manifest(t, rel), "pkg"); d.Match() {
		t.Fatalf("symlink replaced by a file matched")
	}
}

func TestCompareExclusionsApplyOnlyToPathsTheArchiveLacks(t *testing.T) {
	rel := tree(t, map[string]string{"pkg/hooks.py": "1", "pkg/public/dist/shipped.js": "release"})
	inst := tree(t, map[string]string{
		"pkg/hooks.py": "1", "pkg/public/dist/shipped.js": "release", "pkg/public/dist/built.js": "b",
		"pkg.egg-info/PKG-INFO": "x", "pkg/__pycache__/h.pyc": "x", "node_modules/a/b.js": "x", ".DS_Store": "x",
	})
	d := Compare(manifest(t, inst), manifest(t, rel), "pkg")
	if !d.Match() {
		t.Fatalf("generated files should be ignored: %+v", d)
	}
	_ = os.WriteFile(filepath.Join(inst, "pkg/public/dist/shipped.js"), []byte("edited"), 0o644)
	d = Compare(manifest(t, inst), manifest(t, rel), "pkg")
	if !reflect.DeepEqual(d.Changed, []string{"pkg/public/dist/shipped.js"}) {
		t.Fatalf("a release file under an excluded pattern must still be compared: %+v", d)
	}
}

func TestCompareDirectoryOnlyHoldingExcludedOutputIsNotAnAddition(t *testing.T) {
	rel := tree(t, map[string]string{"pkg/hooks.py": "1"})
	inst := tree(t, map[string]string{"pkg/hooks.py": "1", "pkg/public/dist/app.js": "built"})
	if d := Compare(manifest(t, inst), manifest(t, rel), "pkg"); !d.Match() {
		t.Fatalf("diff = %+v", d)
	}
	// ...but an empty extra directory, or one holding a real file, still counts.
	_ = os.MkdirAll(filepath.Join(inst, "empty"), 0o755)
	_ = os.WriteFile(filepath.Join(inst, "pkg", "mine.py"), []byte("x"), 0o644)
	d := Compare(manifest(t, inst), manifest(t, rel), "pkg")
	if !reflect.DeepEqual(d.Added, []string{"empty", "pkg/mine.py"}) {
		t.Fatalf("diff = %+v", d)
	}
}

func TestBuildManifestSkipsHashingWhenAsked(t *testing.T) {
	root := tree(t, map[string]string{"a.py": "1", "node_modules/x.js": "2"})
	m, err := BuildManifest(root, func(rel string) bool { return rel != "node_modules/x.js" })
	if err != nil {
		t.Fatal(err)
	}
	if m["a.py"].SHA256 == "" || m["node_modules/x.js"].SHA256 != "" || m["node_modules/x.js"].Type != TypeFile {
		t.Fatalf("manifest = %+v", m)
	}
}
