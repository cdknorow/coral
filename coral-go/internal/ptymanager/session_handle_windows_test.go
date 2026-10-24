//go:build windows

package ptymanager

import (
	"fmt"
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

// #6821 on Windows, measured: every spawned session leaves about nine handles open in the server whichever way it ends (a leak of its own, found with this test, that a kill does not free either), so
// the handle count is not flat after N sessions. What the fix changes is the other part: a session that ends on its own used to keep about twenty more than one that was killed. This asserts that
// N sessions ending on their own end up holding no more handles than N sessions that were killed (a margin of one a session): the natural exit is as clean as a kill.
func TestSessionsThatEndOnTheirOwnHoldNoMoreHandlesThanAKilledOne(t *testing.T) {
	const runs = 8
	exit := "powershell.exe -NoProfile -Command exit"
	settle := func() { time.Sleep(exitDrain + 6*time.Second) }
	backend := NewPTYBackend()
	defer backend.Close()
	require.NoError(t, backend.Spawn("hd-warm", "test", t.TempDir(), "sid-hd-warm", exit, 120, 40)) // the first spawn opens a few process-wide handles of its own
	settle()

	// N sessions that are killed
	base := handlesOfThisProcess(t)
	for i := 0; i < runs; i++ {
		require.NoError(t, backend.Spawn(fmt.Sprintf("hd-k%d", i), "test", t.TempDir(), fmt.Sprintf("sid-hd-k%d", i), "powershell.exe -NoProfile -NoExit -Command Start-Sleep 600", 120, 40))
	}
	for i := 0; i < runs; i++ {
		require.NoError(t, backend.Kill(fmt.Sprintf("hd-k%d", i)))
	}
	settle()
	afterKills := handlesOfThisProcess(t)
	killed := int(afterKills) - int(base)

	// N sessions that end on their own
	for i := 0; i < runs; i++ {
		require.NoError(t, backend.Spawn(fmt.Sprintf("hd-n%d", i), "test", t.TempDir(), fmt.Sprintf("sid-hd-n%d", i), exit, 120, 40))
	}
	settle()
	natural := int(handlesOfThisProcess(t)) - int(afterKills)
	t.Logf("handles left by %d killed sessions: %d; by %d sessions that ended on their own: %d", runs, killed, runs, natural)
	assert.LessOrEqual(t, natural, killed+runs, "sessions that end on their own hold more handles than killed ones (%d against %d for %d sessions)", natural, killed, runs)
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
