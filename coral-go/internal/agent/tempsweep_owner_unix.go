//go:build !windows

package agent

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether info's file belongs to this process's user.
func ownedByCurrentUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
