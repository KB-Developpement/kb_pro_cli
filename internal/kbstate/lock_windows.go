//go:build windows

package kbstate

import "errors"

// Lock is unavailable on Windows: syscall.Flock does not exist there, and kb
// only ships for linux and darwin.
type Lock struct{}

// AcquireLock always fails on Windows.
func AcquireLock(root string) (*Lock, error) {
	return nil, errors.New("bench locking is not supported on windows")
}

// Release is a no-op.
func (l *Lock) Release() {}

// ProcessAlive cannot be answered on Windows and reports false.
func ProcessAlive(pid int) bool { return false }
