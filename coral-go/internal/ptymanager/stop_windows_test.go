//go:build windows

package ptymanager

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/naming"
)

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// #68: stopping a terminal ends the whole tree, not only the process ConPTY was given: a program the shell started is gone
// too (the job object).
func TestWindowsStopEndsTheWholeProcessTree(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	b := NewPTYBackend()
	name := "claude-68-tree"
	require.NoError(t, b.Spawn(name, "claude", t.TempDir(), "0068", "powershell.exe -NoProfile", 120, 40))
	time.Sleep(2 * time.Second)
	// A program under the shell that a shell exit alone would not end.
	script := "$p = Start-Process ping -ArgumentList '-n','300','127.0.0.1' -PassThru -WindowStyle Hidden; $p.Id | Out-File -Encoding ascii '" +
		strings.ReplaceAll(pidFile, "'", "''") + "'; Wait-Process -Id $p.Id"
	require.NoError(t, b.SendInput(name, []byte(script+EnterKey)))
	var child int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && child > 0
	}, 20*time.Second, 200*time.Millisecond, "the program was started")
	require.True(t, processAlive(child))

	// A program is still running under the shell, so the polite stop leaves the end to the force kill after the grace.
	require.NoError(t, b.Kill(name))
	assert.False(t, b.IsRunning(name))
	require.Eventually(t, func() bool { return !processAlive(child) }, 5*time.Second, 100*time.Millisecond, "the program under the shell is ended with the tree")
}

func TestWindowsIdleShellStopsWithinThePoliteStop(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	b := NewPTYBackend()
	name := "claude-68-idle"
	require.NoError(t, b.Spawn(name, "claude", t.TempDir(), "0069", "powershell.exe -NoProfile", 120, 40))
	time.Sleep(2 * time.Second)
	start := time.Now()
	require.NoError(t, b.Kill(name))
	assert.Less(t, time.Since(start), 3*time.Second, "an idle shell is told to exit once the Ctrl-Cs are in, not killed after 5 s")
}
