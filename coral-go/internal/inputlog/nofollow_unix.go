//go:build !windows

package inputlog

import "syscall"

// openNoFollow makes opening the log fail if its path is a symlink.
const openNoFollow = syscall.O_NOFOLLOW
