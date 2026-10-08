//go:build !kbfault

// Package fault holds the kill points used by the crash-recovery tests.
// In a normal build every hook is a no-op that the compiler removes.
package fault

// Hit is a kill point. It does nothing outside the kbfault build tag.
func Hit(point string) {}
