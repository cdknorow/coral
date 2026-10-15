//go:build !windows

package agent

import "syscall"

// settingsOpenFlags: opening a settings file never follows a symlink or waits on a FIFO.
const settingsOpenFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
