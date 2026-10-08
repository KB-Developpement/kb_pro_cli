//go:build !windows

package kbstate

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// Lock is the bench-scoped mutation lock: an advisory flock on .kb/lock.
// The kernel drops it when the process exits or is killed. The file itself is
// never deleted, because deleting it would break mutual exclusion between a
// holder and a newcomer that opened the old inode.
type Lock struct {
	f *os.File
}

// AcquireLock creates .kb (0700) and takes an exclusive, non-blocking flock on
// .kb/lock (0600). A second holder fails at once with a *LockHeldError naming
// the PID the holder wrote after it acquired the lock.
func AcquireLock(root string) (*Lock, error) {
	if err := EnsureKB(root); err != nil {
		return nil, err
	}
	path := LockPath(root)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder, _ := HolderPID(root)
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, &LockHeldError{PID: holder, Path: path}
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	_ = f.Chmod(0o600)
	// The PID is for diagnostics only; it is written after the lock is held.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
		_ = f.Sync()
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. The lock file stays.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// ProcessAlive reports whether a process with this PID exists.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
