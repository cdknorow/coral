//go:build windows

package ptymanager

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/terminalinput"
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
	require.NoError(t, b.SendInput(terminalinput.Unclassified, name, []byte(script+EnterKey)))
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

// #5706 (Security's answer a): the stop's control keys are in the input debug log: the first Ctrl-C (session.killWithin), the second
// one and the exit line (politeStop, through noteStopWrite), all Coral's own; and recording them costs the stop no time.
func TestWindowsStopRecordsItsControlKeysAndKeepsItsTiming(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	b := NewPTYBackend()
	name := "claude-5706-stop"
	require.NoError(t, b.Spawn(name, "claude", t.TempDir(), "5706", "powershell.exe -NoProfile", 120, 40))
	time.Sleep(2 * time.Second)
	start := time.Now()
	require.NoError(t, b.Kill(name))
	assert.Less(t, time.Since(start), 3*time.Second, "the same polite stop as without the log")
	inputlog.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	log := string(raw)
	assert.GreaterOrEqual(t, strings.Count(log, "target="+name+" "), 3, log)
	assert.GreaterOrEqual(t, strings.Count(log, `kind=keys keys="C-c"`), 2, "the first and the second Ctrl-C: %s", log)
	assert.Contains(t, log, `kind=text len=4 text="exit"`)
}

// #5665: the root's image is read from its process handle. A started cmd.exe is a shell; a started ping.exe (an agent started directly) is not;
// a process that does not exist is not.
func TestRootIsShellReadsTheImageOfTheProcess(t *testing.T) {
	sh := exec.Command("cmd.exe", "/c", "ping -n 20 127.0.0.1 >nul")
	require.NoError(t, sh.Start())
	defer func() { _ = sh.Process.Kill(); _ = sh.Wait() }()
	assert.True(t, rootIsShell(sh.Process.Pid), "cmd.exe is a shell")

	direct := exec.Command("ping.exe", "-n", "20", "127.0.0.1")
	require.NoError(t, direct.Start())
	defer func() { _ = direct.Process.Kill(); _ = direct.Wait() }()
	assert.False(t, rootIsShell(direct.Process.Pid), "ping.exe started directly is not a shell, as an agent started directly is not")

	assert.False(t, rootIsShell(os.Getpid()), "the test binary is not a shell")
	assert.False(t, rootIsShell(0x7ffffff0), "a process that does not exist is not one")
}
