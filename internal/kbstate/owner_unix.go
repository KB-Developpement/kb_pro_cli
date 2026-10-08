//go:build !windows

package kbstate

import (
	"os"
	"syscall"
)

// pathOwner returns the uid owning path.
func pathOwner(path string) (uid int, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, false
	}
	return int(st.Uid), true
}
