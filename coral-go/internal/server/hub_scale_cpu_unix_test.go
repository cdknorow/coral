//go:build !windows

package server

import (
	"syscall"
	"time"
)

// processCPU is this process's user and system CPU time so far (hub_scale_test.go).
func processCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
