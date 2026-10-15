//go:build !windows

package ptymanager

import (
	"os"
	"syscall"
)

// oNoFollowLog makes opening an agent log fail on a symlink as its last name, and never wait
// on a FIFO planted in its place.
const oNoFollowLog = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// ownedByMe reports whether this process's user owns the file (an old log in the shared temp dir).
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
