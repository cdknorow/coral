//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// inForeground reports whether this process's group is the terminal's foreground group: a
// background job that reads the terminal is stopped (SIGTTIN) instead of getting input.
func inForeground(tty *os.File) bool {
	pgrp, err := unix.IoctlGetInt(int(tty.Fd()), unix.TIOCGPGRP)
	return err == nil && pgrp == unix.Getpgrp()
}
