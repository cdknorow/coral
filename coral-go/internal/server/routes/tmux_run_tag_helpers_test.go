//go:build !windows

package routes

import (
	"strconv"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

func atoiOr0(s string) int { n, _ := strconv.Atoi(s); return n }

// killIfStillRecorded SIGKILLs pid only while its start time is the one it had when first seen, so a failed run leaves no helper behind and a reused pid is never signalled.
var recordedStarts = map[int]int64{}

func rememberStart(pid int) {
	if p, err := process.NewProcess(int32(pid)); err == nil {
		if c, err := p.CreateTime(); err == nil {
			recordedStarts[pid] = c
		}
	}
}

func killIfStillRecorded(pid int) {
	want, ok := recordedStarts[pid]
	if !ok {
		return
	}
	if p, err := process.NewProcess(int32(pid)); err == nil {
		if c, err := p.CreateTime(); err == nil && c == want {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func gonePID(pid int, within time.Duration) bool {
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(25 * time.Millisecond) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
	}
	return false
}
