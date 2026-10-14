package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
)

// MaintenanceManifest is a versioned recovery artifact. Higher versions must
// never be used as proof by an older CLI.
type MaintenanceManifest struct {
	SchemaVersion int                        `json:"schema_version"`
	Entries       []MaintenanceManifestEntry `json:"entries"`
}

// MaintenanceManifestEntry records the complete tree, including directory
// modes and symlink targets. Files are hashed without following symlinks.
type MaintenanceManifestEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size,omitempty"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256,omitempty"`
	Target string `json:"target,omitempty"`
}

func maintenanceEnsureTree(root, dir string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("maintenance path outside bench")
	}
	cur := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		cur = filepath.Join(cur, part)
		fi, e := os.Lstat(cur)
		if errors.Is(e, os.ErrNotExist) {
			if e = os.Mkdir(cur, 0700); e != nil {
				return e
			}
			if e = fsutil.SyncDir(filepath.Dir(cur)); e != nil {
				return e
			}
			continue
		}
		if e != nil {
			return e
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("maintenance path %s is not a real directory", cur)
		}
		if e = os.Chmod(cur, 0700); e != nil {
			return e
		}
	}
	return nil
}

func maintenanceManifest(root string) ([]MaintenanceManifestEntry, uint64, error) {
	entries := []MaintenanceManifestEntry{}
	var total uint64
	err := filepath.WalkDir(root, func(p string, de os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		e := MaintenanceManifestEntry{Path: filepath.ToSlash(rel), Mode: uint32(fi.Mode())}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			e.Kind = "symlink"
			e.Target, err = os.Readlink(p)
			if err != nil {
				return err
			}
		case fi.IsDir():
			e.Kind = "directory"
		case fi.Mode().IsRegular():
			e.Kind = "file"
			e.Size = fi.Size()
			e.SHA256, err = fileSHA256(p)
			if err != nil {
				return err
			}
			if fi.Size() < 0 || total > ^uint64(0)-uint64(fi.Size()) {
				return errors.New("app size overflow")
			}
			total += uint64(fi.Size())
		default:
			return fmt.Errorf("cannot recover special file %s", p)
		}
		entries = append(entries, e)
		return nil
	})
	return entries, total, err
}

func copyMaintenanceTree(src, dst string, entries []MaintenanceManifestEntry) error {
	for _, e := range entries {
		sp := filepath.Join(src, filepath.FromSlash(e.Path))
		dp := filepath.Join(dst, filepath.FromSlash(e.Path))
		mode := os.FileMode(e.Mode)
		switch e.Kind {
		case "directory":
			if e.Path != "." {
				if err := os.Mkdir(dp, 0700); err != nil {
					return err
				}
			}
		case "symlink":
			if err := os.Symlink(e.Target, dp); err != nil {
				return err
			}
		case "file":
			in, err := os.Open(sp)
			if err != nil {
				return err
			}
			out, err := os.OpenFile(dp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				in.Close()
				return err
			}
			_, err = io.Copy(out, in)
			closeErr := in.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = out.Chmod(mode.Perm() | mode&os.ModeSetuid | mode&os.ModeSetgid | mode&os.ModeSticky)
			}
			if err == nil {
				err = out.Sync()
			}
			closeErr = out.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
		}
	}
	// Apply directory modes last, so read-only source directories remain copyable.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Kind == "directory" {
			m := os.FileMode(e.Mode)
			if err := os.Chmod(filepath.Join(dst, filepath.FromSlash(e.Path)), m.Perm()|m&os.ModeSetuid|m&os.ModeSetgid|m&os.ModeSticky); err != nil {
				return err
			}
		}
	}
	return syncMaintenanceTree(dst)
}

func syncMaintenanceTree(root string) error {
	dirs := []string{}
	err := filepath.WalkDir(root, func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if de.Type().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			err = f.Sync()
			closeErr := f.Close()
			if err != nil {
				return err
			}
			return closeErr
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err = fsutil.SyncDir(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

var maintenanceAvailableSpace = maintenanceFreeSpace

// RecoveryCopyMaintenance retains and verifies every byte, including ignored
// files, reflogs and unreachable Git objects. Incomplete copies are retained.
func RecoveryCopyMaintenance(root, directory, sessionID string) (path, manifest string, err error) {
	if !maintenanceComponent(sessionID) {
		return "", "", errors.New("invalid session id")
	}
	app, err := maintenanceAppPath(root, directory)
	if err != nil {
		return "", "", err
	}
	if err = ownMaintenanceGit(app); err != nil {
		return "", "", err
	}
	before, size, err := maintenanceManifest(app)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	parent := filepath.Join(root, ".kb", "recovery", sessionID)
	if err = maintenanceEnsureTree(root, parent); err != nil {
		return "", "", err
	}
	free, err := maintenanceAvailableSpace(parent)
	if err != nil {
		return "", "", err
	}
	if size > ^uint64(0)/2 || free < size*2 {
		return "", "", fmt.Errorf("recovery needs at least twice the app size (%d bytes), available %d", size, free)
	}
	path, err = os.MkdirTemp(parent, directory+"-"+time.Now().UTC().Format("20060102T150405Z")+"-")
	if err != nil {
		return "", "", err
	}
	manifest = path + ".manifest.json"
	if err = copyMaintenanceTree(app, path, before); err != nil {
		return path, manifest, err
	}
	if err = verifyMaintenanceManifest(app, path, before); err != nil {
		return path, manifest, err
	}
	raw, err := json.MarshalIndent(MaintenanceManifest{SchemaVersion: 1, Entries: before}, "", "  ")
	if err != nil {
		return path, manifest, err
	}
	if err = fsutil.WriteFileDurable(manifest, raw, 0600); err != nil {
		return path, manifest, err
	}
	return path, manifest, nil
}

func verifyMaintenanceManifest(source, copy string, expected []MaintenanceManifestEntry) error {
	live, _, err := maintenanceManifest(source)
	if err != nil {
		return err
	}
	recovered, _, err := maintenanceManifest(copy)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, live) {
		return errors.New("source changed while making recovery copy")
	}
	if !reflect.DeepEqual(expected, recovered) {
		return errors.New("recovery manifest does not match source")
	}
	return nil
}

// VerifyMaintenanceRecovery rechecks a retained copy immediately before a swap.
func VerifyMaintenanceRecovery(source, copy, manifest string) error {
	raw, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	var saved MaintenanceManifest
	if err = json.Unmarshal(raw, &saved); err != nil {
		return err
	}
	if saved.SchemaVersion != 1 {
		return fmt.Errorf("unsupported recovery manifest schema_version %d", saved.SchemaVersion)
	}
	entries := saved.Entries
	if len(entries) == 0 {
		return errors.New("empty recovery manifest")
	}
	return verifyMaintenanceManifest(source, copy, entries)
}
