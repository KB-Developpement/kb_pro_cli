// Package adopt compares an installed app tree with a release archive for
// `kb adopt` (contracts 7.3). It is pure: it reads two directories and reports
// differences by path name only.
package adopt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
)

// Entry types.
const (
	TypeFile    = "file"
	TypeDir     = "dir"
	TypeSymlink = "symlink"
	TypeOther   = "other"
)

// Entry is what is compared for one path. Permission bits other than the
// executable bit are deliberately absent: umask differs between install and
// adopt.
type Entry struct {
	Type   string
	Exec   bool   // any of 0o111, regular files only
	SHA256 string // regular files only
	Target string // symlinks only; never followed
}

// Manifest maps a slash-separated path relative to the app root to its Entry.
type Manifest map[string]Entry

// BuildManifest walks root without following symlinks. needHash, when not nil,
// says whether a regular file's content has to be hashed; skipping files that
// can never be compared (a huge node_modules) keeps adopt fast. A file that is
// not hashed has an empty SHA256 and must not be compared.
func BuildManifest(root string, needHash func(rel string) bool) (Manifest, error) {
	m := Manifest{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			m[rel] = Entry{Type: TypeSymlink, Target: target}
		case info.IsDir():
			m[rel] = Entry{Type: TypeDir}
		case mode.IsRegular():
			sum := ""
			if needHash == nil || needHash(rel) {
				var err error
				if sum, err = hashFile(path); err != nil {
					return err
				}
			}
			m[rel] = Entry{Type: TypeFile, Exec: mode.Perm()&0o111 != 0, SHA256: sum}
		default:
			m[rel] = Entry{Type: TypeOther}
		}
		return nil
	})
	return m, err
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Diff is the outcome of comparing an installed tree with one candidate.
type Diff struct {
	Added    []string // installed, not in the archive, not excluded
	Removed  []string // in the archive, missing from the installed tree
	Changed  []string // in both, but type, exec bit, content or target differs
	Compared int      // regular files and symlinks compared and equal or not
}

// Match reports a full match.
func (d Diff) Match() bool { return len(d.Added)+len(d.Removed)+len(d.Changed) == 0 }

// Compare compares the installed manifest with an archive manifest. The
// exclusion patterns apply only to installed paths the archive does not
// contain: a path the archive contains is always compared normally, so an
// exclusion can never hide release content.
//
// A directory that is not in the archive is reported only when it is empty: if
// it has contents, those are judged one by one (excluded ones ignored, others
// named). Otherwise a parent that a build created only to hold excluded output,
// such as <package>/public when only public/dist was built, would make a
// pristine tree look edited.
func Compare(installed, archive Manifest, pkg string) Diff {
	hasChild := map[string]bool{}
	for p := range installed {
		for parent := path.Dir(p); parent != "." && parent != "/"; parent = path.Dir(parent) {
			hasChild[parent] = true
		}
	}
	var d Diff
	for p, ie := range installed {
		ae, inArchive := archive[p]
		if !inArchive {
			if kbstate.Excluded(p, ie.Type == TypeDir, pkg) {
				continue
			}
			if ie.Type == TypeDir && hasChild[p] {
				continue
			}
			d.Added = append(d.Added, p)
			continue
		}
		if ie.Type != TypeDir {
			d.Compared++
		}
		if ie != ae {
			d.Changed = append(d.Changed, p)
		}
	}
	for p := range archive {
		if _, ok := installed[p]; !ok {
			d.Removed = append(d.Removed, p)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Changed)
	return d
}

// String summarises a diff for logs and tests.
func (d Diff) String() string {
	return fmt.Sprintf("added=%d removed=%d changed=%d compared=%d", len(d.Added), len(d.Removed), len(d.Changed), d.Compared)
}
