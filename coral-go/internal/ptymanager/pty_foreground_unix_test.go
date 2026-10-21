//go:build !windows

package ptymanager

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// #3219, PTY backend: the session's shell is idle at its prompt, busy while a
// command (the agent CLI) runs, and idle again after it exits.
func TestPTYShellIdleFollowsTheCommand(t *testing.T) {
	t.Setenv("CORAL_SHELL", "/bin/sh")
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	name := "claude-" + exitedSID
	if err := b.Spawn(name, "claude", t.TempDir(), exitedSID, "", 80, 24); err != nil {
		skipIfPTYSpawnDenied(t, err)
		require.NoError(t, err)
	}
	idle := func() bool { v, _, ok := b.ShellIdle(name); return ok && v }
	busy := func() bool { v, _, ok := b.ShellIdle(name); return ok && !v }

	require.Eventually(t, idle, 5*time.Second, 25*time.Millisecond, "a fresh terminal is at an idle shell")
	require.NoError(t, b.SendLine(terminalinput.Unclassified, name, "sleep 30"))
	require.Eventually(t, busy, 5*time.Second, 25*time.Millisecond, "a running command keeps it busy")
	require.NoError(t, b.SendInput(terminalinput.Unclassified, name, []byte{0x03})) // Ctrl-C
	require.Eventually(t, idle, 5*time.Second, 25*time.Millisecond, "idle again after it exits")

	_, _, ok := b.ShellIdle("claude-nope")
	assert.False(t, ok, "an unknown session reports nothing")
}
