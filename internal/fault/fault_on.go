//go:build kbfault

// Package fault holds the kill points used by the crash-recovery tests.
package fault

import (
	"os"
	"syscall"
)

// Hit kills the process with SIGKILL when KB_FAULT names this point. It exists
// only in test builds (-tags kbfault); the release binary never contains it.
func Hit(point string) {
	if os.Getenv("KB_FAULT") != point {
		return
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {} // SIGKILL is not catchable; never run on past the kill point
}
