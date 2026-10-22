//go:build !windows

package server

import (
	"os"
	"syscall"
)

// endThisServer ends this server the way a person would, with SIGTERM: main's graceful shutdown runs (agents' state is saved, the listener is closed). An update's relaunch uses it after
// the updater program was started; that program waits for this process to end before it replaces the install.
func endThisServer() { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) }
