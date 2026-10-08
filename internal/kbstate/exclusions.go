package kbstate

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// Exclusions returns the generated-file patterns applied when comparing an
// installed tree with a release archive and when counting staged entries
// (contracts 7.3). pkg is the app's Python package.
func Exclusions(pkg string) []string {
	return []string{"*.egg-info/", "__pycache__/", "*.pyc", "node_modules/", pkg + "/public/dist/", ".DS_Store"}
}

// Excluded reports whether the slash-separated path rel (relative to the app
// root) matches an exclusion pattern. isDir says whether rel is a directory.
func Excluded(rel string, isDir bool, pkg string) bool {
	comps := strings.Split(rel, "/")
	dirComps := comps
	if !isDir && len(comps) > 0 {
		dirComps = comps[:len(comps)-1]
	}
	for _, c := range dirComps {
		if c == "__pycache__" || c == "node_modules" || strings.HasSuffix(c, ".egg-info") {
			return true
		}
	}
	if !isDir {
		base := comps[len(comps)-1]
		if base == ".DS_Store" || strings.HasSuffix(base, ".pyc") {
			return true
		}
	}
	if len(comps) >= 3 && pkg != "" && comps[0] == pkg && comps[1] == "public" && comps[2] == "dist" {
		return true
	}
	return false
}

// CountEntries counts the files, directories and symlinks under root, not
// counting root itself and skipping everything Excluded matches. The same
// count at staging time and at replay time is what lets a replay tell the tree
// it finds is the one the journal staged, whatever the build added since.
func CountEntries(root, pkg string) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if Excluded(rel, d.IsDir(), pkg) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		n++
		return nil
	})
	return n, err
}
