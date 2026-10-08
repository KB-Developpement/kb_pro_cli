//go:build !windows

package kbstate

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLockHolderHelper is not a test: it is the body of the subprocess that
// holds the bench lock for the tests below. It does nothing unless
// KB_TEST_LOCK_ROOT is set.
func TestLockHolderHelper(t *testing.T) {
	root := os.Getenv("KB_TEST_LOCK_ROOT")
	if root == "" {
		t.Skip("helper process only")
	}
	lk, err := AcquireLock(root)
	if err != nil {
		os.Stdout.WriteString("error " + err.Error() + "\n")
		os.Exit(3)
	}
	_ = lk
	os.Stdout.WriteString("ready\n")
	time.Sleep(time.Minute)
}

func startHolder(t *testing.T, root string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHolderHelper$")
	cmd.Env = append(os.Environ(), "KB_TEST_LOCK_ROOT="+root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("lock holder did not start: %q err=%v", line, err)
	}
	return cmd
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

// A second holder fails within a second, names the first holder's PID; after
// SIGKILL of the holder the next mutation acquires within a second and the
// lock file keeps its inode (it is never deleted).
func TestLockSecondHolderNamesPIDAndSurvivesSIGKILL(t *testing.T) {
	root := t.TempDir()
	holder := startHolder(t, root)
	before := inode(t, LockPath(root))

	start := time.Now()
	_, err := AcquireLock(root)
	if time.Since(start) > time.Second {
		t.Fatalf("second holder took %v to fail", time.Since(start))
	}
	var held *LockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("AcquireLock = %v, want *LockHeldError", err)
	}
	if held.PID != holder.Process.Pid {
		t.Fatalf("error names PID %d, holder is %d", held.PID, holder.Process.Pid)
	}
	if !strings.Contains(err.Error(), "PID") {
		t.Fatalf("message does not mention the PID: %v", err)
	}

	if err := holder.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_, _ = holder.Process.Wait()

	start = time.Now()
	lk, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("AcquireLock after SIGKILL of the holder: %v", err)
	}
	defer lk.Release()
	if time.Since(start) > time.Second {
		t.Fatalf("acquire after SIGKILL took %v", time.Since(start))
	}
	if after := inode(t, LockPath(root)); after != before {
		t.Fatalf("lock inode changed %d -> %d: the lock file was deleted and recreated", before, after)
	}
}

func TestLockModesAndPIDLine(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(Dir(root), 0o755); err != nil { // a pre-existing, too-open .kb
		t.Fatal(err)
	}
	lk, err := AcquireLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	for path, want := range map[string]os.FileMode{Dir(root): 0o700, LockPath(root): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, fi.Mode().Perm(), want)
		}
	}
	if pid, ok := HolderPID(root); !ok || pid != os.Getpid() {
		t.Fatalf("HolderPID = %d, %v; want %d", pid, ok, os.Getpid())
	}
	lk.Release()
	if _, err := os.Stat(LockPath(root)); err != nil {
		t.Fatalf("Release deleted the lock file: %v", err)
	}
}
