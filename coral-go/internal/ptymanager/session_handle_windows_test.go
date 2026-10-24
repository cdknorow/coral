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
// (about 10 handles a session without the fix, none with it). The count is not flat from the first session: the first batch of ConPTY sessions costs the process a one-time ~9 handles a session (runtime
// threads and their events), so a first batch of sessions that end on their own warms the process up, and the second batch of the same kind may add no more than one handle a session.
func TestASecondBatchOfSessionsThatEndOnTheirOwnAddsNoHandles(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	const runs = 8
	settle := func() { time.Sleep(exitDrain + 6*time.Second) }
	backend := NewPTYBackend()
	defer backend.Close()
	batch := func(tag string) {
		for i := 0; i < runs; i++ {
			require.NoError(t, backend.Spawn(fmt.Sprintf("hd-%s%d", tag, i), "test", t.TempDir(), fmt.Sprintf("sid-hd-%s%d", tag, i), exitNow, 120, 40))
		}
		settle()
	}
	batch("a") // the first batch pays the one-time cost
	before := handleCount(t)
	batch("b")
	added := handleCount(t) - before
	t.Logf("handles added by a second batch of %d sessions that ended on their own: %d", runs, added)
	assert.LessOrEqual(t, added, runs, "a second batch of %d sessions that ended on their own added %d handles: something is left open for each", runs, added)
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
