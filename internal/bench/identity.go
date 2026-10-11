package bench

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// validateArchiveIdentity runs before swapping source or invoking Bench. Bench
// renames mismatched project folders during requirements setup, outside our
// recorded transaction paths. Such archives must never reach that operation.
func validateArchiveIdentity(root, directory, pkg string) error {
	data, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil // Legacy setup.py archives retain their existing path.
	}
	if err != nil {
		return fmt.Errorf("read archive project identity: %w", err)
	}
	var metadata struct {
		Project struct {
			Name string `toml:"name"`
		} `toml:"project"`
	}
	if err := toml.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("invalid archive pyproject.toml: %w", err)
	}
	name := metadata.Project.Name
	// Bench compares these strings exactly before deciding to rename a folder.
	if name != directory || name != pkg {
		return fmt.Errorf("archive project %q does not match directory %q and package %q — refusing before Bench can rename source", name, directory, pkg)
	}
	info, err := os.Stat(filepath.Join(root, pkg, "__init__.py"))
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("archive lacks Python package %q — refusing before source replacement", pkg)
	}
	return nil
}
