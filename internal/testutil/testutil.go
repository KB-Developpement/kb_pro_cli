// Package testutil holds helpers shared by the tests of several packages. It is
// imported only from _test.go files.
package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TreeDigest is a SHA-256 over a sorted manifest of every entry under dir:
// relative path, type, mode, symlink target and content hash.
// A missing dir has the digest "absent".
func TreeDigest(t testing.TB, dir string) string {
	t.Helper()
	return TreeDigestExcluding(t, dir, nil)
}

// TreeDigestExcluding is TreeDigest skipping paths for which skip returns true.
func TreeDigestExcluding(t testing.TB, dir string, skip func(rel string) bool) string {
	t.Helper()
	if _, err := os.Lstat(dir); err != nil {
		return "absent"
	}
	var lines []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if skip != nil && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			lines = append(lines, fmt.Sprintf("%s|link|%o|%s", rel, info.Mode().Perm(), target))
		case info.IsDir():
			lines = append(lines, fmt.Sprintf("%s|dir|%o", rel, info.Mode().Perm()))
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			lines = append(lines, fmt.Sprintf("%s|file|%o|%s", rel, info.Mode().Perm(), hex.EncodeToString(sum[:])))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", dir, err)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// BenchDigest is the digest over apps/ and sites/ together, excluding logs/,
// *.pyc and __pycache__/.
func BenchDigest(t testing.TB, root string) string {
	t.Helper()
	skip := func(rel string) bool {
		return strings.HasSuffix(rel, ".pyc") || strings.Contains(rel, "__pycache__") || strings.HasPrefix(rel, "logs")
	}
	parts := []string{
		TreeDigestExcluding(t, filepath.Join(root, "apps"), skip),
		TreeDigestExcluding(t, filepath.Join(root, "sites"), skip),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// File is one regular file of a fixture archive.
type File struct {
	Name string
	Body string
	Mode int64 // 0 means 0644
}

// TarGz builds a release-shaped .tar.gz (one top-level directory "top") in a
// fresh temp dir and returns its path and bytes.
func TarGz(t testing.TB, top string, files []File) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	write := func(h *tar.Header, body string) {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(&tar.Header{Name: top + "/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	made := map[string]bool{}
	for _, f := range files {
		for dir := filepath.ToSlash(filepath.Dir(f.Name)); dir != "." && dir != "/" && !made[dir]; dir = filepath.ToSlash(filepath.Dir(dir)) {
			made[dir] = true
			write(&tar.Header{Name: top + "/" + dir + "/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		write(&tar.Header{Name: top + "/" + f.Name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(f.Body))}, f.Body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), top+".tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, buf.Bytes()
}

// Sha256Hex is the hex SHA-256 of data.
func Sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// IncompressibleBody returns n bytes that gzip cannot shrink (deterministic).
func IncompressibleBody(n int) string {
	var b strings.Builder
	state := uint64(0x9E3779B97F4A7C15)
	for b.Len() < n {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		b.WriteString(fmt.Sprintf("%016x", state))
	}
	return b.String()[:n]
}

// IsolateGit makes git ignore the user's and the system's configuration for
// the duration of the test (and for child processes the code under test starts).
func IsolateGit(t testing.TB) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

// Git runs real git in dir and fails the test on error.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, errb.String())
	}
	return out.String()
}

// StockClone builds a clean stock-Frappe-looking checkout at dir: one commit on
// main, remote origin = url, upstream origin/main at HEAD. No network.
func StockClone(t testing.TB, dir, url string) {
	t.Helper()
	IsolateGit(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("stock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "add", ".")
	Git(t, dir, "commit", "-q", "-m", "init")
	Git(t, dir, "remote", "add", "origin", url)
	Git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	Git(t, dir, "config", "branch.main.remote", "origin")
	Git(t, dir, "config", "branch.main.merge", "refs/heads/main")
}

// CopyOf reads a whole file or fails.
func MustRead(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var _ = io.EOF
