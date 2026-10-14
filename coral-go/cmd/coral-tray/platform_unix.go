//go:build !windows

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// redirectStderr duplicates the log file's fd onto stderr (fd 2) so that
// native/CGO crash messages (e.g. from systray or Cocoa frameworks) are
// captured in tray.log instead of being lost. x/sys/unix.Dup2 exists on every
// Unix target (on linux/arm64 and riscv64, which have no dup2 syscall, it is
// dup3), where syscall.Dup2 does not (#3450).
func redirectStderr(f *os.File) {
	_ = unix.Dup2(int(f.Fd()), int(os.Stderr.Fd()))
}

// detachProcessAttrs returns SysProcAttr for running a detached background process.
func detachProcessAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// signalProcess sends a signal to a process. Used for --stop and PID liveness checks.
func signalProcess(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}
