package bench

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarEntry is one member of an in-memory tar.gz fixture.
type tarEntry struct {
	Name     string
	Typeflag byte
	Mode     int64
	Linkname string
	Body     string
}

func writeTarGz(t *testing.T, entries []tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		mode := e.Mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     e.Name,
			Typeflag: e.Typeflag,
			Mode:     mode,
			Linkname: e.Linkname,
			Size:     int64(len(e.Body)),
		}
		if e.Typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractTarGzStripped_NormalTree(t *testing.T) {
	archive := writeTarGz(t, []tarEntry{
		{Name: "app-1.0/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "app-1.0/marker.txt", Typeflag: tar.TypeReg, Body: "NEW"},
		{Name: "app-1.0/bin/run.sh", Typeflag: tar.TypeReg, Mode: 0o755, Body: "#!/bin/sh\n"},
		{Name: "app-1.0/link.txt", Typeflag: tar.TypeSymlink, Linkname: "marker.txt"},
	})
	dest := t.TempDir()
	if err := extractTarGzStripped(archive, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "marker.txt"))
	if err != nil || string(data) != "NEW" {
		t.Fatalf("marker.txt = %q, err %v", data, err)
	}
	fi, err := os.Stat(filepath.Join(dest, "bin", "run.sh"))
	if err != nil {
		t.Fatalf("run.sh: %v", err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode = %04o, want 0755", fi.Mode().Perm())
	}
	if lt, err := os.Readlink(filepath.Join(dest, "link.txt")); err != nil || lt != "marker.txt" {
		t.Errorf("relative symlink inside the tree should be kept: %q %v", lt, err)
	}
}

func TestExtractTarGzStripped_RejectsEscapes(t *testing.T) {
	tests := []struct {
		name    string
		entries []tarEntry
	}{
		{"dotdot", []tarEntry{{Name: "app/../../escape", Typeflag: tar.TypeReg, Body: "x"}}},
		{"absolute", []tarEntry{{Name: "/etc/passwd", Typeflag: tar.TypeReg, Body: "x"}}},
		{"symlink escape", []tarEntry{
			{Name: "app/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "app/out", Typeflag: tar.TypeSymlink, Linkname: "../../../../.."},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			err := extractTarGzStripped(writeTarGz(t, tc.entries), dest)
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
		})
	}
}

// A symlink pointing at an absolute path outside the staging dir followed by a
// member written through it must never write outside the staging dir.
func TestExtractTarGzStripped_SymlinkThenFileThrough(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "pwned.txt")
	archive := writeTarGz(t, []tarEntry{
		{Name: "app/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "app/esc", Typeflag: tar.TypeSymlink, Linkname: outside},
		{Name: "app/esc/pwned.txt", Typeflag: tar.TypeReg, Body: "owned"},
	})
	dest := t.TempDir()
	err := extractTarGzStripped(archive, dest)
	if _, statErr := os.Stat(victim); statErr == nil {
		t.Fatalf("file written outside the staging dir at %s (err=%v)", victim, err)
	}
	if err == nil {
		// skipped rather than rejected — then the payload must live inside dest
		if _, statErr := os.Stat(filepath.Join(dest, "esc", "pwned.txt")); statErr != nil {
			t.Fatalf("payload neither rejected nor contained: %v", statErr)
		}
	}
}

// The bench helpers must not shell out for extraction.
func TestUpdateFromArchive_NoTarBinaryNeeded(t *testing.T) {
	root, archive := makeTempBench(t, "kb_test")
	t.Setenv("KB_BENCH_ROOT", root)
	t.Setenv("PATH", t.TempDir()) // empty PATH: neither tar nor bench exist

	_, err := UpdateFromArchive(context.Background(), archive, "kb_test")
	if err == nil {
		t.Fatal("expected failure at the bench step")
	}
	if strings.Contains(err.Error(), "extract archive") {
		t.Fatalf("extraction must not depend on an external tar: %v", err)
	}
	if got := marker(t, filepath.Join(root, "apps", "kb_test")); got != "OLD" {
		t.Errorf("app content = %q, want OLD", got)
	}
}
