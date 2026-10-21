//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/subgentic/coral/internal/safego"
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

// askTrayToShowWindow is a second start of the app asking the running tray (pid) to show its window: SIGUSR1.
func askTrayToShowWindow(pid int) error {
	return syscall.Kill(pid, syscall.SIGUSR1)
}

// onShowWindowSignal calls show whenever another start of the app asks this tray to show its window.
func onShowWindowSignal(show func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		defer safego.Recover("coral-tray/platform_unix:showWindowSignal")
		for range ch {
			show()
		}
	}()
}
