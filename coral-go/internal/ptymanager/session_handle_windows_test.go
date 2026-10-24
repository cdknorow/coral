//go:build windows

package ptymanager

import (
	"os/exec"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

var procGetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

// handleCount is the number of handles this process holds (the Windows counterpart of counting open descriptors).
func handlesOfThisProcess(t *testing.T) uint32 {
	t.Helper()
	var n uint32
	r, _, err := procGetProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n)))
	require.NotZero(t, r, "GetProcessHandleCount: %v", err)
	return n
}

// #6821 on Windows: sessions whose process ends on its own must not keep a pseudo console, its pipes and the job object open. After the exit and the drain the process's handle count
// is back where it was (a margin for the runtime's own churn).
func TestSessionsThatEndOnTheirOwnLeaveNoHandlesOpen(t *testing.T) {
	backend := NewPTYBackend()
	defer backend.Close()
	require.NoError(t, backend.Spawn("hd-warm", "test", t.TempDir(), "sid-hd-warm", "Write-Output done", 80, 24)) // the first spawn opens a few process-wide handles of its own
	time.Sleep(exitDrain + 2*time.Second)
	before := handlesOfThisProcess(t)

	const runs = 12
	for i := 0; i < runs; i++ {
		require.NoError(t, backend.Spawn("hd-"+string(rune('a'+i)), "test", t.TempDir(), "sid-hd-"+string(rune('a'+i)), "Write-Output done", 80, 24))
	}
	var now uint32
	ok := assert.Eventually(t, func() bool { now = handlesOfThisProcess(t); return now <= before+12 }, 30*time.Second, 250*time.Millisecond,
		"handles after %d sessions ended on their own: %d (before %d)", runs, now, before)
	t.Logf("handles before %d, after %d sessions ended on their own: %d (settled: %v)", before, runs, handlesOfThisProcess(t), ok)
}

func pingProcesses() int {
	out, _ := exec.Command("tasklist", "/FI", "IMAGENAME eq ping.exe", "/NH").Output()
	return strings.Count(strings.ToLower(string(out)), "ping.exe")
}

// What a session leaves running when its own process ends (not an assertion, a record for the review of #6821): a hidden ping started from the session's shell, which then exits at once.
func TestRecordWhatSessionLeavesRunningWhenItsProcessEnds(t *testing.T) {
	backend := NewPTYBackend()
	defer backend.Close()
	before := pingProcesses()
	require.NoError(t, backend.Spawn("hd-desc", "test", t.TempDir(), "sid-hd-desc",
		"Start-Process -FilePath ping.exe -ArgumentList '-n','60','127.0.0.1' -WindowStyle Hidden; Write-Output spawned", 80, 24))
	time.Sleep(3 * time.Second)
	during := pingProcesses()
	time.Sleep(exitDrain + 4*time.Second)
	after := pingProcesses()
	t.Logf("RECORD ping.exe processes: before the session %d, 3 s after it started %d, 4+ s after its own process ended %d (a descendant surviving the session's natural exit shows as after > before)", before, during, after)
	exec.Command("taskkill", "/IM", "ping.exe", "/F").Run() // the probe's own leftovers only: this is a throwaway runner
}
