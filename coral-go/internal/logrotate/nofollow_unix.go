//go:build !windows

package logrotate

import "syscall"

// openNoFollow: a symlink planted at the log's path is not followed.
const openNoFollow = syscall.O_NOFOLLOW
