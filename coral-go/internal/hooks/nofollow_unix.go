//go:build !windows

package hooks

import "syscall"

// openNoFollow makes opening a cache file fail if its path is a symlink.
const openNoFollow = syscall.O_NOFOLLOW
