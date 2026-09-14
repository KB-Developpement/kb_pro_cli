//go:build windows

package bench

import "syscall"

// detachedSysProcAttr has no session concept on Windows; the process is started
// without a console of its own.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
