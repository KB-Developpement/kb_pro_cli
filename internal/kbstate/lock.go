package kbstate

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LockHeldError is returned when another process holds .kb/lock.
type LockHeldError struct {
	PID  int // 0 when the holder has not written its PID yet
	Path string
}

func (e *LockHeldError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("another kb operation is running on this bench (PID %d, lock %s) — wait for it to finish", e.PID, e.Path)
	}
	return fmt.Sprintf("another kb operation is running on this bench (lock %s, holder PID not recorded yet) — wait for it to finish", e.Path)
}

// HolderPID reads the PID line in .kb/lock. It only reads: it takes no lock and
// creates nothing. ok is false when the file is missing or names no PID.
func HolderPID(root string) (pid int, ok bool) {
	data, err := os.ReadFile(LockPath(root))
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
