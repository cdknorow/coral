//go:build !windows

package secfile

import (
	"os"
	"syscall"
)

// OpenNoFollow is os.OpenFile that never goes through a symbolic link at the path (O_NOFOLLOW:
// a link there makes the open fail). See the Windows version for the folders an agent can write.
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW, perm)
}
