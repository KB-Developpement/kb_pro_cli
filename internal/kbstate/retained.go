package kbstate

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Retained is a recovery copy kb keeps and never deletes.
type Retained struct {
	Path string
	Kind string // "legacy" or "pre-receipt"
	Size int64  // bytes of regular files, symlinks counted by their own size
}

// ListRetained lists .kb/recovery/legacy/* and .kb/recovery/*-pre-receipt-*
// with their sizes. It reads only.
func ListRetained(root string) []Retained {
	var out []Retained
	if entries, err := os.ReadDir(LegacyDir(root)); err == nil {
		for _, e := range entries {
			p := filepath.Join(LegacyDir(root), e.Name())
			out = append(out, Retained{Path: p, Kind: "legacy", Size: TreeSize(p)})
		}
	}
	if entries, err := os.ReadDir(RecoveryDir(root)); err == nil {
		for _, e := range entries {
			if strings.Contains(e.Name(), "-pre-receipt-") {
				p := filepath.Join(RecoveryDir(root), e.Name())
				out = append(out, Retained{Path: p, Kind: "pre-receipt", Size: TreeSize(p)})
			}
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Path < out[k].Path })
	return out
}

// TreeSize sums the sizes of everything under p without following symlinks.
func TreeSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, infoErr := d.Info(); infoErr == nil && !d.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}
