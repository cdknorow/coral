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

// LinkCount is the open file's hard-link count; ok is false when the platform doesn't say.
func LinkCount(_ *os.File, fi os.FileInfo) (n uint64, ok bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink), true
	}
	return 0, false
}
