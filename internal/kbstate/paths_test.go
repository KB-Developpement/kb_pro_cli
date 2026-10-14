package kbstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDirCreatesMissingParentChain(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "new", "nested", "target")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "new"), filepath.Join(root, "new", "nested"), dir} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s = %v, err=%v; want directory mode 0700", path, info, err)
		}
	}
}

func TestEnsureDirPreservesExistingParentMode(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "existing")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o751); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(filepath.Join(parent, "new", "child")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(parent)
	if err != nil || info.Mode().Perm() != 0o751 {
		t.Fatalf("existing parent mode = %v, err=%v; want 0751", info, err)
	}
}

func TestEnsureDirRefusesRegularFileWithoutChangingMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(path); err == nil {
		t.Fatal("regular file was accepted as directory")
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode().Perm() != 0o640 {
		t.Fatalf("regular file changed: info=%v err=%v; want mode 0640", info, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "keep" {
		t.Fatalf("regular file contents changed: got=%q err=%v", got, err)
	}
}
