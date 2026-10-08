//go:build windows

package kbstate

func pathOwner(path string) (uid int, ok bool) { return 0, false }
