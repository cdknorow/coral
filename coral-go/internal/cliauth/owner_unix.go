//go:build !windows

package cliauth

import (
	"os"
	"syscall"
)

// privateMode: not readable or writable by group or others.
func privateMode(fi os.FileInfo) bool { return fi.Mode().Perm()&0o077 == 0 }

func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return !ok || int(st.Uid) == os.Getuid()
}
