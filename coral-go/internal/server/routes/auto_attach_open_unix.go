//go:build !windows

package routes

import (
	"os"
	"syscall"
)

// linkCount is the opened file's hard-link count.
func linkCount(_ *os.File, fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}
