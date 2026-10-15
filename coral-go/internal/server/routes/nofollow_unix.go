//go:build !windows

package routes

import "syscall"

// oNoFollow makes an open fail on a symlink (the last path element).
const oNoFollow = syscall.O_NOFOLLOW
