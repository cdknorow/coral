//go:build !windows

package routes

import (
	"os"
	"syscall"
)

// attachOpenFlags: the open fails on a symlink (the last path element) and never
// blocks on a FIFO.
const attachOpenFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// linkCount is the file's hard-link count.
func linkCount(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}
