//go:build !windows

package safefs

import (
	"os"
	"syscall"
)

// oNoFollow: an open fails on a symlink as the last name. oNonBlock: opening a FIFO returns at
// once instead of waiting for a writer.
const (
	oNoFollow = syscall.O_NOFOLLOW
	oNonBlock = syscall.O_NONBLOCK
)

// fileLinks is a file's hard-link count (1 when the platform doesn't say).
func fileLinks(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
