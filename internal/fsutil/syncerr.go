package fsutil

import (
	"errors"
	"syscall"
)

// isSyncUnsupported reports an fsync on a directory that the filesystem or
// platform does not support (EINVAL, ENOTSUP, EBADF on some network mounts).
func isSyncUnsupported(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EBADF)
}
