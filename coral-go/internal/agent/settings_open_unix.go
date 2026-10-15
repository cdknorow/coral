//go:build !windows

package agent

import "syscall"

// settingsOpenFlags: opening the user's settings file never waits on a FIFO. A symlink is
// followed (dotfiles setups link ~/.claude/settings.json); the handle must still be a
// regular file.
const settingsOpenFlags = syscall.O_NONBLOCK
