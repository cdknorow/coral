//go:build !windows

package background

import "syscall"

// oNoFollow makes an open fail on a symlink (the last path element).
const oNoFollow = syscall.O_NOFOLLOW

// oNonBlock makes opening a FIFO for reading return at once instead of waiting for a writer.
const oNonBlock = syscall.O_NONBLOCK
