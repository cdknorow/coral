//go:build !windows

package stripstatic

import (
	"fmt"
	"os"
	"syscall"
)

// checkPrivate refuses a folder or file another user could have changed: not owned by the caller, or open to group or others (a folder: any access; a file: any write).
func checkPrivate(p string, st os.FileInfo) error {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by the user running the build", p)
	}
	if st.IsDir() && st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is open to other users (mode %o): the overlay folder must be owner-only", p, st.Mode().Perm())
	}
	if !st.IsDir() && st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s can be written by other users (mode %o)", p, st.Mode().Perm())
	}
	return nil
}
