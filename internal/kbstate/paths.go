// Package kbstate owns the bench-local `.kb/` tree: the mutation lock, the
// transaction journal, per-app receipts and the retained recovery copies. It
// knows nothing about downloads or bench commands; callers pass the bench root.
package kbstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
)

// SupportedSchema is the highest schema_version of any .kb/*.json file this
// build can read and write. Files with a higher version are never overwritten.
const SupportedSchema = 1

// Layout names under <bench>/.kb/.
const (
	dirName       = ".kb"
	lockName      = "lock"
	journalName   = "journal.json"
	appsDirName   = "apps"
	recoveryName  = "recovery"
	legacyDirName = "legacy"
)

// Dir is <bench>/.kb.
func Dir(root string) string { return filepath.Join(root, dirName) }

// LockPath is <bench>/.kb/lock.
func LockPath(root string) string { return filepath.Join(Dir(root), lockName) }

// JournalPath is <bench>/.kb/journal.json.
func JournalPath(root string) string { return filepath.Join(Dir(root), journalName) }

// ReceiptsDir is <bench>/.kb/apps.
func ReceiptsDir(root string) string { return filepath.Join(Dir(root), appsDirName) }

// ReceiptPath is <bench>/.kb/apps/<directory>.json.
func ReceiptPath(root, directory string) string {
	return filepath.Join(ReceiptsDir(root), directory+".json")
}

// RecoveryDir is <bench>/.kb/recovery.
func RecoveryDir(root string) string { return filepath.Join(Dir(root), recoveryName) }

// LegacyDir is <bench>/.kb/recovery/legacy.
func LegacyDir(root string) string { return filepath.Join(RecoveryDir(root), legacyDirName) }

// UTCStamp is the compact UTC time used in recovery paths: YYYYMMDDTHHMMSSZ.
func UTCStamp(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// EnsureDir creates missing parents, sets dir to 0700 and durably publishes its
// directory entry. Existing entries are synced too, so retries cannot bypass a
// previous failed directory sync.
func EnsureDir(dir string) error {
	dir = filepath.Clean(dir)
	parent := filepath.Dir(dir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if _, err := os.Stat(parent); os.IsNotExist(err) {
			if err := EnsureDir(parent); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	} else if err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	if err := fsutil.SyncDir(dir); err != nil {
		return err
	}
	return fsutil.SyncDir(parent)
}

// EnsureKB creates <bench>/.kb with mode 0700.
func EnsureKB(root string) error { return EnsureDir(Dir(root)) }

// EnsureTree creates dir and every missing ancestor below <bench>/.kb with
// mode 0700. dir must be inside <bench>/.kb.
func EnsureTree(root, dir string) error {
	kb := Dir(root)
	rel, err := filepath.Rel(kb, dir)
	if err != nil || rel == ".." || strings.HasPrefix(filepath.ToSlash(rel), "../") {
		return fmt.Errorf("%s is not inside %s", dir, kb)
	}
	if err := EnsureKB(root); err != nil {
		return err
	}
	cur := kb
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, part)
		if err := EnsureDir(cur); err != nil {
			return err
		}
	}
	return nil
}

// CleanTempFiles removes the temp files an interrupted atomic write (a crash
// between create and rename) left in .kb and .kb/apps. It runs under the lock,
// so no live writer can own one.
func CleanTempFiles(root string) {
	for _, dir := range []string{Dir(root), ReceiptsDir(root)} {
		matches, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
}
