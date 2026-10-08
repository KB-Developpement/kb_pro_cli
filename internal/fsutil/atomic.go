// Package fsutil holds small filesystem helpers shared across the CLI.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so a reader never observes a truncated or half-written
// file and a crash mid-write leaves the previous content intact.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	// CreateTemp makes the file 0600; apply the caller's mode explicitly so the
	// result does not depend on umask.
	if err := os.Chmod(tmpPath, mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

// WriteFileDurable is WriteFileAtomic plus an fsync of the containing
// directory, so the rename itself survives a crash or power loss. Use it for
// files that carry recovery state (journal, receipts). WriteFileAtomic stays as
// it is for the license cache and apps.txt.
func WriteFileDurable(path string, data []byte, mode os.FileMode) error {
	if err := WriteFileAtomic(path, data, mode); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(path))
}

// SyncDir fsyncs a directory so entries created or renamed in it are durable.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s for sync: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems refuse fsync on a directory; the rename already
		// happened, so this is not worth failing a transaction over.
		if isSyncUnsupported(err) {
			return nil
		}
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}
