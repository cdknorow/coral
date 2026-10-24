//go:build windows

package ptymanager

import (
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

var procGetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

// handleCount is the number of handles this process holds (the Windows counterpart of counting open descriptors).
func handleCount(t *testing.T) uint32 {
	t.Helper()
	var n uint32
	r, _, err := procGetProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n)))
	require.NotZero(t, r, "GetProcessHandleCount: %v", err)
	return n
}

// #6821 on Windows: sessions whose process ends on its own must not keep a pseudo console, its pipes and the job object open. After the exit and the drain the process's handle count is back where it was
// (a margin for the runtime's own churn). Measured on a Windows runner (Security Reviewer Windows, #6827): 8 sessions ending on their own add 80 handles without the fix and 0 with it; the first batch of ConPTY
// sessions costs the process a one-time ~9 handles a session, so a warm-up session is not enough and the bound is +12, not a tight one.
func TestSessionsThatEndOnTheirOwnLeaveNoHandlesOpen(t *testing.T) {
	backend := NewPTYBackend()
	defer backend.Close()
	require.NoError(t, backend.Spawn("hd-warm", "test", t.TempDir(), "sid-hd-warm", "Write-Output done", 80, 24)) // the first spawn opens a few process-wide handles of its own
	time.Sleep(exitDrain + 2*time.Second)
	before := handleCount(t)

	const runs = 12
	for i := 0; i < runs; i++ {
		require.NoError(t, backend.Spawn("hd-"+string(rune('a'+i)), "test", t.TempDir(), "sid-hd-"+string(rune('a'+i)), "Write-Output done", 80, 24))
	}
	var now uint32
	ok := assert.Eventually(t, func() bool { now = handleCount(t); return now <= before+12 }, 30*time.Second, 250*time.Millisecond,
		"handles after %d sessions ended on their own: %d (before %d)", runs, now, before)
	t.Logf("handles before %d, after %d sessions ended on their own: %d (settled: %v)", before, runs, handleCount(t), ok)
}

// The shell exits but a program it started keeps running: the exit watcher still closes the pseudo console after the drain, while that program lives (a pseudo console's output pipe only ends when it is closed).
// Measured on the Windows runner: fails on the parent (266 handles against 255), passes with the fix.
func TestHandlesAreFreedWhenTheShellExitsButAChildRuns(t *testing.T) {
	backend := NewPTYBackend()
	defer backend.Close()
	require.NoError(t, backend.Spawn("hd-warm2", "test", t.TempDir(), "sid-hd-warm2", "Write-Output done", 80, 24))
	time.Sleep(exitDrain + 3*time.Second)
	before := handleCount(t)

	// the shell starts a hidden ping that runs for ~12 s and exits at once
	require.NoError(t, backend.Spawn("hd-child", "test", t.TempDir(), "sid-hd-child",
		"Start-Process -FilePath ping.exe -ArgumentList '-n','12','127.0.0.1' -WindowStyle Hidden; Write-Output spawned", 80, 24))
	var now uint32
	assert.Eventually(t, func() bool { now = handleCount(t); return now <= before+12 }, 9*time.Second, 250*time.Millisecond,
		"handles while the left-behind program still runs: %d (before %d)", now, before)
}
