//go:build !windows

package agent

import "syscall"

// oNoFollow makes OpenFile refuse a symlink at the final path component (#3582).
const oNoFollow = syscall.O_NOFOLLOW
