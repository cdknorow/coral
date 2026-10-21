//go:build !windows

package routes

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity is a file's (device, inode): the same for every hard link to it.
func fileIdentity(_ string, fi os.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), true
}

// fileLinks is a file's hard-link count (#3554: a shared diff object nothing links has 1).
func fileLinks(_ string, fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
