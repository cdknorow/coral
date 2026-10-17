//go:build !windows

package startup

import (
	"os"
	"syscall"
)

// ownedByThisUser reports whether info's file belongs to this process's user.
func ownedByThisUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
