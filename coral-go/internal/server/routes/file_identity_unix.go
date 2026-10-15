//go:build !windows

package routes

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity is a file's (device, inode): the same for every hard link to it.
func fileIdentity(fi os.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), true
}
