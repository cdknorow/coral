//go:build !windows

package background

import "syscall"

// oNoFollow makes an open fail on a symlink (the last path element).
const oNoFollow = syscall.O_NOFOLLOW
