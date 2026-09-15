package bench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBenchStartCmd_NoShell(t *testing.T) {
	root := "/tmp/bench root; touch /tmp/pwned"
	cmd := benchStartCmd(root)
	if base := filepath.Base(cmd.Path); base != "bench" {
		t.Errorf("cmd.Path base = %q, want \"bench\" (must not go through a shell)", base)
	}
	if want := []string{"bench", "start"}; !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("cmd.Args = %q, want %q", cmd.Args, want)
	}
	if cmd.Dir != root {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, root)
	}
	if cmd.SysProcAttr == nil {
		t.Error("SysProcAttr should request a new session so the server survives")
	}
}

func TestBenchStartLogPath(t *testing.T) {
	got := benchStartLogPath("/opt/bench")
	if want := filepath.Join("/opt/bench", "logs", "bench-start.log"); got != want {
		t.Errorf("log path = %q, want %q", got, want)
	}
}

// fakeBenchOnPath installs a `bench` shell script as the only executable on
// PATH. The script records its arguments and its working directory, then writes
// a marker to stdout so the caller can tell it actually ran.
func fakeBenchOnPath(t *testing.T) (argsFile, cwdFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake bench script requires a POSIX shell")
	}
	binDir, outDir := t.TempDir(), t.TempDir()
	argsFile = filepath.Join(outDir, "args")
	cwdFile = filepath.Join(outDir, "cwd")

	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\npwd -P > %q\necho bench-start-ran\n", argsFile, cwdFile)
	if err := os.WriteFile(filepath.Join(binDir, "bench"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	return argsFile, cwdFile
}

// waitForFile polls path until it has content or the deadline passes.
func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return strings.TrimSpace(string(data))
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s — the detached bench process never wrote it", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestStartDevServer proves the dev server can be started from scratch (not
// only restarted): `bench start` is launched from the bench root and its output
// is redirected into logs/bench-start.log.
func TestStartDevServer(t *testing.T) {
	root := t.TempDir()
	argsFile, cwdFile := fakeBenchOnPath(t)
	t.Setenv("KB_BENCH_ROOT", root)

	if err := StartDevServer(context.Background()); err != nil {
		t.Fatalf("StartDevServer: %v", err)
	}

	logPath := benchStartLogPath(root)
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log file %s was not created: %v", logPath, err)
	}

	if got := waitForFile(t, logPath); !strings.Contains(got, "bench-start-ran") {
		t.Errorf("log file content = %q, want it to contain the child's stdout", got)
	}
	if got := waitForFile(t, argsFile); got != "start" {
		t.Errorf("bench args = %q, want \"start\"", got)
	}

	wantCwd, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForFile(t, cwdFile); got != wantCwd {
		t.Errorf("bench start cwd = %q, want the bench root %q", got, wantCwd)
	}
}
