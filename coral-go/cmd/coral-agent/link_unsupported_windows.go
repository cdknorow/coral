//go:build windows

package main

import (
	"errors"
	"syscall"
)

// linkUnsupported reports whether err says the file system cannot make a hard link there: FAT and exFAT answer ERROR_INVALID_FUNCTION or ERROR_NOT_SUPPORTED, a share on
// another volume ERROR_NOT_SAME_DEVICE, some shares ERROR_ACCESS_DENIED.
func linkUnsupported(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case 1 /* ERROR_INVALID_FUNCTION */, 17 /* ERROR_NOT_SAME_DEVICE */, 50 /* ERROR_NOT_SUPPORTED */, 5 /* ERROR_ACCESS_DENIED */ :
		return true
	}
	return false
}
