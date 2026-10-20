//go:build !windows

package verify

import (
	"fmt"
	"os"
	"syscall"
)

// checkPrivate refuses a file another user could have written or read: not owned by this user, or with any group or other permission bit.
func checkPrivate(fi os.FileInfo) error {
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("mode %o is not owner-only", fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("owned by uid %d, not this user", st.Uid)
	}
	return nil
}
