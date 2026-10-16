//go:build !windows

package ptymanager

import (
	"os"
	"syscall"
)

// oNonBlockLog: opening an agent log never waits on a FIFO planted in its place (a link there is
// refused by secfile.OpenNoFollow).
const oNonBlockLog = syscall.O_NONBLOCK

// ownedByMe reports whether this process's user owns the file (an old log in the shared temp dir).
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
