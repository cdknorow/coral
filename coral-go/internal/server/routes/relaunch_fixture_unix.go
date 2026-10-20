//go:build relaunchfixture && !windows

package routes

import (
	"os"
	"syscall"
)

// fixtureUID is who must own the fixture folder and file (a test replaces it).
var fixtureUID = os.Geteuid

// ownerUID is the user id that owns the file.
func ownerUID(fi os.FileInfo) (int, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), true
	}
	return 0, false
}
