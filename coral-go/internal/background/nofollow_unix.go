//go:build !windows

package background

import (
	"os"
	"syscall"
)

// oNoFollow makes an open fail on a symlink (the last path element).
const oNoFollow = syscall.O_NOFOLLOW

// oNonBlock makes opening a FIFO for reading return at once instead of waiting for a writer.
const oNonBlock = syscall.O_NONBLOCK

// fileLinks is a file's hard-link count (1 when the platform doesn't say).
func fileLinks(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
