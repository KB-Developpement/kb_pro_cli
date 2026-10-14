//go:build darwin

package bench

import "golang.org/x/sys/unix"

func maintenanceRenameExclusive(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}
func maintenanceFreeSpace(path string) (uint64, error) {
	var st unix.Statfs_t
	err := unix.Statfs(path, &st)
	return st.Bavail * uint64(st.Bsize), err
}
