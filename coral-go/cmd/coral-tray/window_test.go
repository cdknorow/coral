//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/windowmark"
)

// #5827: launching Coral.app by hand opens the window; a start nobody asked for stays in the background; launching it again while it runs hands over to the running one.
func TestOnlyALaunchByAPersonOpensTheWindow(t *testing.T) {
	assert.True(t, opensWindowAtStart(false, false), "Finder, Dock, Launchpad, Spotlight, open Coral.app")
	assert.False(t, opensWindowAtStart(true, false), "the relaunch after an update passes --no-browser")
	assert.False(t, opensWindowAtStart(false, true), "a login item")
	assert.False(t, opensWindowAtStart(true, true))
}

func TestASecondStartHandsOverToTheRunningTray(t *testing.T) {
	read := func(pid int) func(string) int { return func(string) int { return pid } }
	assert.Equal(t, 4242, otherTrayPID("/d", 100, false, read(4242)), "another tray of this data folder runs: the second start hands over")
	assert.Zero(t, otherTrayPID("/d", 100, false, read(0)), "none runs: this start is the first")
	assert.Zero(t, otherTrayPID("/d", 100, false, read(100)), "the pid file names this process itself")
	assert.Zero(t, otherTrayPID("/d", 100, true, read(4242)), "a restart replaces the running tray (it waits for it to exit): no hand-over")

	var asked []int
	assert.True(t, handOver(4242, func(pid int) error { asked = append(asked, pid); return nil }))
	assert.Equal(t, []int{4242}, asked)
	assert.False(t, handOver(4242, func(int) error { return errors.New("no such process") }), "a tray that cannot be asked: this start runs as before")
}

// The real hand-over: the running tray gets SIGUSR1 from askTrayToShowWindow and shows its window.
func TestAskingARunningTrayToShowItsWindowReachesIt(t *testing.T) {
	shown := make(chan struct{}, 4)
	onShowWindowSignal(func() { shown <- struct{}{} })
	t.Cleanup(func() { signal.Reset(syscall.SIGUSR1) })
	require.NoError(t, askTrayToShowWindow(os.Getpid()))
	select {
	case <-shown:
	case <-time.After(3 * time.Second):
		t.Fatal("the window was not asked for")
	}
	// a process that is gone cannot be asked
	cmd := exec.Command("sleep", "0")
	require.NoError(t, cmd.Run())
	assert.Error(t, askTrayToShowWindow(cmd.Process.Pid), "a finished process")
}

// #5827: the window is recorded open from when its process starts until the person closes it; when the tray ends it itself (an update, Quit) the record stays, so the relaunch
// after an update opens the window again.
func TestTheWindowIsRecordedOpenUntilThePersonClosesIt(t *testing.T) {
	dir := t.TempDir()
	run := func(current bool) {
		cmd := exec.Command("sleep", "0.3")
		require.NoError(t, cmd.Start())
		done := make(chan struct{})
		go func() { trackWindow(dir, cmd, func() bool { return current }); close(done) }()
		require.Eventually(t, func() bool { return windowmark.Present(dir) }, 2*time.Second, 10*time.Millisecond, "recorded open while it runs")
		<-done
	}
	run(true) // the person closed it: still the tray's current window when it exited
	assert.False(t, windowmark.Present(dir), "closed by the person: not recorded")
	run(false) // the tray let go of it first (killCoralApp)
	assert.True(t, windowmark.Present(dir), "ended by the tray: recorded, so an update's relaunch opens the window")
}
