//go:build windows

package bench

import "errors"

func maintenanceRenameExclusive(from, to string) error {
	return errors.New("maintenance is unsupported on Windows")
}
func maintenanceFreeSpace(path string) (uint64, error) {
	return 0, errors.New("maintenance is unsupported on Windows")
}
