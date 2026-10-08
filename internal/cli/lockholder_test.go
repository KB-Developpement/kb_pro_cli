//go:build !windows

package cli

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
)

// TestLockHolderHelper is the body of a subprocess that holds the bench lock.
// It does nothing unless KB_TEST_LOCK_ROOT is set.
func TestLockHolderHelper(t *testing.T) {
	root := os.Getenv("KB_TEST_LOCK_ROOT")
	if root == "" {
		t.Skip("helper process only")
	}
	if _, err := kbstate.AcquireLock(root); err != nil {
		os.Stdout.WriteString("error " + err.Error() + "\n")
		os.Exit(3)
	}
	os.Stdout.WriteString("ready\n")
	time.Sleep(time.Minute)
}

// startLockHolder runs a subprocess holding root's lock and returns its PID.
func startLockHolder(t *testing.T, root string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHolderHelper$")
	cmd.Env = append(os.Environ(), "KB_TEST_LOCK_ROOT="+root)
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("lock holder did not start: %q err=%v", line, err)
	}
	return cmd.Process.Pid
}
