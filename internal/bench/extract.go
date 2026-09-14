package bench

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxArchiveFileSize caps a single member of a source archive. App sources are
// a few MiB; anything beyond this is a decompression bomb, not a release.
const maxArchiveFileSize = 512 << 20 // 512 MiB

// extractTarGzStripped extracts a .tar.gz into destDir, dropping the first path
// component of every member (the single top-level directory that GitHub source
// tarballs carry) — the semantics of `tar --strip-components=1`.
//
// It is deliberately not a shell-out to tar: server-supplied archives are
// untrusted, and this implementation refuses anything that could write outside
// destDir — absolute names, "..", symlinks/hardlinks resolving outside the
// staging tree — and ignores devices, fifos and other special members.
func extractTarGzStripped(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("decompress archive: %w", err)
	}
	defer gr.Close()

	root, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("resolve staging dir: %w", err)
	}

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}

		rel, keep, err := stripFirstComponent(hdr.Name)
		if err != nil {
			return err
		}
		if !keep {
			continue
		}
		target := filepath.Join(root, rel)
		if !withinDir(root, target) {
			return fmt.Errorf("archive member %q escapes the staging directory", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, dirMode(hdr.Mode)); err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := writeArchiveFile(tr, target, fileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := linkArchiveEntry(hdr, target, root, false); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := linkArchiveEntry(hdr, target, root, true); err != nil {
				return err
			}
		default:
			// Devices, fifos, sockets and metadata entries: nothing to install.
		}
	}
}

// stripFirstComponent validates a tar member name and drops its leading path
// component. keep is false for entries that are only the top-level directory.
func stripFirstComponent(name string) (rel string, keep bool, err error) {
	name = strings.ReplaceAll(name, `\`, "/")
	if name == "" {
		return "", false, nil
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return "", false, fmt.Errorf("archive member %q has an absolute path", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == ".." {
			return "", false, fmt.Errorf("archive member %q contains \"..\"", name)
		}
	}
	if clean == "." {
		return "", false, nil
	}
	_, rest, found := strings.Cut(clean, string(filepath.Separator))
	if !found || rest == "" {
		return "", false, nil // the stripped top-level entry itself
	}
	return rest, true, nil
}

// withinDir reports whether target is root itself or lives under it.
func withinDir(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func fileMode(mode int64) os.FileMode { return os.FileMode(mode) & 0o777 }

func dirMode(mode int64) os.FileMode {
	m := fileMode(mode)
	if m == 0 {
		return 0o755
	}
	return m | 0o700 // we must be able to descend into it while extracting
}

func writeArchiveFile(tr io.Reader, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	if mode == 0 {
		mode = 0o644
	}
	// O_EXCL-less but NOFOLLOW-ish: remove anything already there (including a
	// symlink planted by an earlier member) so we never write through a link.
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	n, err := io.Copy(out, io.LimitReader(tr, maxArchiveFileSize+1))
	if err != nil {
		out.Close()
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", target, err)
	}
	if n > maxArchiveFileSize {
		return fmt.Errorf("archive member %s exceeds the %d byte limit", target, int64(maxArchiveFileSize))
	}
	return nil
}

// linkArchiveEntry creates a symlink or hardlink, refusing any link whose
// destination resolves outside root. Relative links that stay inside the tree
// are kept: app sources legitimately contain some.
func linkArchiveEntry(hdr *tar.Header, target, root string, hard bool) error {
	link := filepath.FromSlash(hdr.Linkname)
	if link == "" {
		return fmt.Errorf("archive member %q has an empty link target", hdr.Name)
	}

	var resolved string
	if filepath.IsAbs(link) {
		resolved = filepath.Clean(link)
	} else if hard {
		// Hardlink names are relative to the archive root, and are stripped the
		// same way as member names.
		rel, keep, err := stripFirstComponent(hdr.Linkname)
		if err != nil || !keep {
			return fmt.Errorf("archive member %q links outside the archive", hdr.Name)
		}
		resolved = filepath.Join(root, rel)
		link = resolved
	} else {
		resolved = filepath.Join(filepath.Dir(target), link)
	}
	if !withinDir(root, resolved) {
		return fmt.Errorf("archive member %q links outside the staging directory (%q)", hdr.Name, hdr.Linkname)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	if hard {
		if err := os.Link(link, target); err != nil {
			return fmt.Errorf("hardlink %s: %w", target, err)
		}
		return nil
	}
	if err := os.Symlink(link, target); err != nil {
		return fmt.Errorf("symlink %s: %w", target, err)
	}
	return nil
}
