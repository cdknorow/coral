//go:build windows

package ptymanager

import (
	"fmt"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/naming"
)

var getProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

// handleCount is how many kernel handles this process holds (the Windows twin of counting open descriptors).
func handleCount(t *testing.T) int {
	t.Helper()
	var n uint32
	r, _, err := getProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n)))
	require.NotZero(t, r, "GetProcessHandleCount: %v", err)
	return int(n)
}

// #6821's Windows twin: a ConPTY session whose shell ends on its own is closed by the exit watcher (the pseudo console's output pipe only ends when the pseudo console is closed, so readLoop alone never returns),
// and with it its handles: after N sessions end on their own the process's handle count is back near where it was.
func TestWindowsSessionsThatEndOnTheirOwnLeaveNoHandlesOpen(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	b := NewPTYBackend()
	defer b.Close()
	require.NoError(t, b.Spawn("handles-warm", "test", t.TempDir(), "sid-handles-warm", "powershell.exe -NoProfile -Command exit", 120, 40))
	time.Sleep(exitDrain + 3*time.Second) // the first spawn opens process-wide handles of its own
	before := handleCount(t)

	const runs = 8
	for i := 0; i < runs; i++ {
		require.NoError(t, b.Spawn(fmt.Sprintf("handles-%d", i), "test", t.TempDir(), fmt.Sprintf("sid-handles-%d", i), "powershell.exe -NoProfile -Command exit", 120, 40))
	}
	require.Eventually(t, func() bool { return handleCount(t) <= before+8 }, 40*time.Second, 250*time.Millisecond,
		"handles after %d sessions ended on their own: %d (before %d): a ConPTY session holds a handful each", runs, handleCount(t), before)
}

// The shell exits but a program it started keeps running: the watcher still closes the pseudo console after the drain, while that program lives.
func TestWindowsHandlesAreFreedWhenTheShellExitsButAChildRuns(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	b := NewPTYBackend()
	defer b.Close()
	require.NoError(t, b.Spawn("handles-warm2", "test", t.TempDir(), "sid-handles-warm2", "powershell.exe -NoProfile -Command exit", 120, 40))
	time.Sleep(exitDrain + 3*time.Second)
	before := handleCount(t)

	// the shell starts a hidden ping that runs for ~12 s and exits at once
	cmd := "powershell.exe -NoProfile -Command Start-Process ping -ArgumentList '-n','12','127.0.0.1' -WindowStyle Hidden"
	require.NoError(t, b.Spawn("handles-child", "test", t.TempDir(), "sid-handles-child", cmd, 120, 40))
	require.Eventually(t, func() bool { return handleCount(t) <= before+4 }, 9*time.Second, 250*time.Millisecond,
		"handles while the left-behind program still runs: %d (before %d)", handleCount(t), before)
}
