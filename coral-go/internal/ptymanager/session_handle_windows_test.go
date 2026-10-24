//go:build windows

package ptymanager

import (
	"fmt"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/naming"
)

var procGetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

// handleCount is the number of handles this process holds (the Windows counterpart of counting open descriptors).
func handleCount(t *testing.T) int {
	t.Helper()
	var n uint32
	r, _, err := procGetProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n)))
	require.NotZero(t, r, "GetProcessHandleCount: %v", err)
	return int(n)
}

// A session is given a full command line here (Spawn does not wrap it in a shell on Windows: a bare cmdlet name is "The system cannot find the file specified").
const exitNow = "powershell.exe -NoProfile -Command exit"

// #6821 on Windows (measured on a Windows runner, #6827): a session whose process ends on its own must be closed like a killed one, or each leaves a pseudo console, its pipes and the job object open
// (80 handles for 8 sessions without the fix, none with it). The absolute count is not flat: the first batch of ConPTY sessions costs the process a one-time ~9 handles a session (runtime threads
// and their events), so this compares: 8 sessions killed first (which pays that cost), then 8 that end on their own, which may hold no more than the killed ones plus one a session.
func TestSessionsThatEndOnTheirOwnHoldNoMoreHandlesThanAKilledOne(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	const runs = 8
	settle := func() { time.Sleep(exitDrain + 6*time.Second) }
	backend := NewPTYBackend()
	defer backend.Close()
	require.NoError(t, backend.Spawn("hd-warm", "test", t.TempDir(), "sid-hd-warm", exitNow, 120, 40))
	settle()

	base := handleCount(t)
	for i := 0; i < runs; i++ {
		require.NoError(t, backend.Spawn(fmt.Sprintf("hd-k%d", i), "test", t.TempDir(), fmt.Sprintf("sid-hd-k%d", i), "powershell.exe -NoProfile -NoExit -Command Start-Sleep 600", 120, 40))
	}
	for i := 0; i < runs; i++ {
		require.NoError(t, backend.Kill(fmt.Sprintf("hd-k%d", i)))
	}
	settle()
	afterKills := handleCount(t)
	killed := afterKills - base

	for i := 0; i < runs; i++ {
		require.NoError(t, backend.Spawn(fmt.Sprintf("hd-n%d", i), "test", t.TempDir(), fmt.Sprintf("sid-hd-n%d", i), exitNow, 120, 40))
	}
	settle()
	natural := handleCount(t) - afterKills
	t.Logf("handles left by %d killed sessions: %d; by %d sessions that ended on their own: %d", runs, killed, runs, natural)
	assert.LessOrEqual(t, natural, killed+runs, "sessions that end on their own hold more handles than killed ones (%d against %d for %d sessions)", natural, killed, runs)
}

// The shell exits but a program it started keeps running: the exit watcher still closes the pseudo console after the drain, while that program lives (a pseudo console's output pipe only ends when it is
// closed). Measured on the Windows runner: fails on the parent of the fix (266 handles against 255), passes with it.
func TestHandlesAreFreedWhenTheShellExitsButAChildRuns(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	backend := NewPTYBackend()
	defer backend.Close()
	require.NoError(t, backend.Spawn("hd-warm2", "test", t.TempDir(), "sid-hd-warm2", exitNow, 120, 40))
	time.Sleep(exitDrain + 3*time.Second)
	before := handleCount(t)

	// the shell starts a hidden ping that runs for ~12 s and exits at once
	cmd := "powershell.exe -NoProfile -Command Start-Process ping -ArgumentList '-n','12','127.0.0.1' -WindowStyle Hidden"
	require.NoError(t, backend.Spawn("hd-child", "test", t.TempDir(), "sid-hd-child", cmd, 120, 40))
	assert.Eventually(t, func() bool { return handleCount(t) <= before+4 }, 9*time.Second, 250*time.Millisecond,
		"handles while the left-behind program still runs: %d (before %d)", handleCount(t), before)
}
