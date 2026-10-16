package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// otherProgram starts a long-lived process of another executable (a different program that
// could have been given a stale tray.pid's number).
func otherProgram(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep 30")
	}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// #3906 (sweep item 7): a pid in tray.pid counts as the tray only while it is this same
// executable; --stop never terminates another program that got the number.
func TestTrayPIDMustBeThisExecutable(t *testing.T) {
	assert.True(t, isTrayProcess(os.Getpid()), "this process: the same executable")

	other := otherProgram(t)
	assert.False(t, isTrayProcess(other.Process.Pid), "another program")

	exited := exec.Command(os.Args[0], "-test.run=^$")
	require.NoError(t, exited.Run())
	assert.False(t, isTrayProcess(exited.Process.Pid), "a process that has exited")

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "tray.pid")
	require.NoError(t, os.WriteFile(pidFile, []byte(strconv.Itoa(other.Process.Pid)), 0o600))
	assert.Equal(t, 0, readPID(dir), "a stale tray.pid naming another program is not a running tray")
	assert.NoFileExists(t, pidFile, "and is removed")

	require.NoError(t, os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600))
	assert.Equal(t, os.Getpid(), readPID(dir))
}

func TestSameExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "coral-tray")
	require.NoError(t, os.WriteFile(exe, nil, 0o700))
	assert.True(t, sameExecutable(exe, exe))
	assert.True(t, sameExecutable(exe+" (deleted)", exe), "Linux's name for a binary replaced since it started")
	assert.False(t, sameExecutable(filepath.Join(dir, "other"), exe))
	if runtime.GOOS == "windows" {
		assert.True(t, sameExecutable(filepath.Join(dir, "CORAL-TRAY"), exe), "Windows paths ignore case")
	}
}
