//go:build !windows

package cmdhandoff

import (
	"os"
	"syscall"
)

// ownedByCurrentUser: the file belongs to the user Coral runs as.
func ownedByCurrentUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
