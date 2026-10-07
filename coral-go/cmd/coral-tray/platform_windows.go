//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// redirectStderr is a no-op on Windows — CGO crash output is already captured
// by the log file redirect in the background spawn path.
func redirectStderr(f *os.File) {}

// detachProcessAttrs returns SysProcAttr for running a detached background process on Windows.
// CREATE_NEW_PROCESS_GROUP detaches the child from the parent's console.
func detachProcessAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000, // CREATE_NO_WINDOW
	}
}

// signalProcess sends a signal to a process on Windows.
// Windows doesn't have Unix signals, so we open and terminate the process directly.
func signalProcess(pid int, sig syscall.Signal) error {
	// Signal 0 is a liveness check
	if sig == 0 {
		p, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		// On Windows, FindProcess always succeeds. We need to try opening
		// the process to verify it exists.
		handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
		if err != nil {
			return fmt.Errorf("process %d not found: %w", pid, err)
		}
		syscall.CloseHandle(handle)
		_ = p
		return nil
	}

	// For SIGTERM, terminate the process
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
