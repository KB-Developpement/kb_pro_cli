//go:build !windows

package bench

import "syscall"

// detachedSysProcAttr puts the child in its own session so it survives the CLI
// exiting and is not killed by signals sent to the CLI's process group.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
