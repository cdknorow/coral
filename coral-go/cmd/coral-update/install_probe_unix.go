//go:build darwin || linux

package main

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"

	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/status"
)

// probeInstall describes this install for the status. The status only reads: the volume is not probed for an atomic swap and writability is the access(2) answer, nothing is created (the
// apply asks both for real before it changes anything).
func probeInstall() *status.Install {
	writable := func(p string) bool { return unix.Access(p, unix.W_OK) == nil }
	switch runtime.GOOS {
	case "darwin":
		return describeInstall(&apply.Mac{SwapSupported: func(string) bool { return true }}, "darwin", writable)
	case "linux":
		return describeInstall(&apply.Linux{Uid: os.Getuid(), SwapSupported: func(string) bool { return true }}, "linux", writable)
	}
	return nil
}
