//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// linkUnsupported reports whether err says the file system cannot make a hard link there (vfat and exfat answer EPERM, others EOPNOTSUPP/ENOTSUP, another volume EXDEV).
func linkUnsupported(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.EPERM || errno == syscall.EXDEV || errno == syscall.ENOTSUP || errno == syscall.EOPNOTSUPP || errno == syscall.EMLINK
}
