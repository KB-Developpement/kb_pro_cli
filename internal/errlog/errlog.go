package errlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	logFileName = "error.log"
	maxLogSize  = 512 * 1024 // 512 KB — truncate the oldest half when exceeded
)

var (
	mu      sync.Mutex
	logDir  string
	logPath string
)

func init() {
	home, _ := os.UserHomeDir()
	logDir = filepath.Join(home, ".config", "kb")
	logPath = filepath.Join(logDir, logFileName)
}

// Log appends a timestamped error entry to ~/.config/kb/error.log.
// It is safe for concurrent use. Errors from the logger itself are silently
// discarded — logging must never break the CLI.
func Log(err error) {
	if err == nil {
		return
	}
	logLine(err.Error())
}

// Logf appends a timestamped formatted message to the error log.
func Logf(format string, args ...any) {
	logLine(fmt.Sprintf(format, args...))
}

func logLine(msg string) {
	mu.Lock()
	defer mu.Unlock()

	_ = os.MkdirAll(logDir, 0700)
	truncateIfNeeded()

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()

	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	fmt.Fprintf(f, "[%s] %s\n", ts, msg)
}

// truncateIfNeeded drops roughly the oldest half of the log when it exceeds maxLogSize.
func truncateIfNeeded() {
	info, err := os.Stat(logPath)
	if err != nil || info.Size() <= maxLogSize {
		return
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return
	}
	// Keep the second half (newest entries).
	half := len(data) / 2
	// Advance to the next newline so we don't cut a line in the middle.
	for half < len(data) && data[half] != '\n' {
		half++
	}
	if half < len(data) {
		half++ // skip the newline itself
	}
	_ = os.WriteFile(logPath, data[half:], 0600)
}
