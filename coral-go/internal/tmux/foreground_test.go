package tmux

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsShellCommand(t *testing.T) {
	for _, c := range []string{"zsh", "-zsh", "bash", "sh", "/bin/zsh", "fish", "dash", "pwsh", "pwsh.exe", "cmd.exe", "ZSH", " bash "} {
		assert.True(t, IsShellCommand(c), c)
	}
	for _, c := range []string{"node", "claude", "codex", "gemini", "python3", "sleep", "vim", "", "zshrc", "bash-language-server"} {
		assert.False(t, IsShellCommand(c), c)
	}
}

// #3219: the agent CLI is typed into the pane's shell at launch. While it (or a
// wrapper script running it) runs, the shell has a child and is not idle; once it
// exits, the shell is idle at its prompt.
func TestAgentShellIdleFollowsTheCLI(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const sid = "3a9e1c52-7b4d-4f0e-9c6a-2d8b1e5f4a70"
	name := "claude-" + sid
	createTestSession(t, c, name, t.TempDir())
	idle := func() bool {
		v, _, err := c.AgentShellIdle(ctx, name, "claude", sid)
		return err == nil && v
	}
	busy := func() bool {
		v, _, err := c.AgentShellIdle(ctx, name, "claude", sid)
		return err == nil && !v
	}

	require.Eventually(t, idle, 5*time.Second, 25*time.Millisecond, "a fresh pane is at an idle shell prompt")

	// A long-running command stands in for the agent CLI.
	require.NoError(t, c.SendKeysToTarget(ctx, name+".0", "sleep 30"))
	require.Eventually(t, busy, 5*time.Second, 25*time.Millisecond, "a running CLI keeps the shell busy")
	require.NoError(t, c.SendRawKeys(ctx, name, []string{"C-c"}, "claude", sid))
	require.Eventually(t, idle, 5*time.Second, 25*time.Millisecond, "idle again once it exits")

	// A CLI started through a shell wrapper script shows 'sh' in the foreground, yet
	// it is running: the pane's shell has a child, so it is not taken as exited.
	require.NoError(t, c.SendKeysToTarget(ctx, name+".0", "sh -c 'sleep 30; true'"))
	require.Eventually(t, func() bool {
		fg, err := c.DisplayMessage(ctx, name+".0", "#{pane_current_command}")
		return err == nil && fg != "" && busy()
	}, 5*time.Second, 25*time.Millisecond, "a wrapper script is not an exited agent")
	require.NoError(t, c.SendRawKeys(ctx, name, []string{"C-c"}, "claude", sid))
	require.Eventually(t, idle, 5*time.Second, 25*time.Millisecond)
}

func TestAgentShellIdleOfAGoneTerminal(t *testing.T) {
	c := newTestClient(t)
	_, _, err := c.AgentShellIdle(context.Background(), "claude-0b1c2d3e-4f50-4617-8293-a4b5c6d7e8f9", "claude", "0b1c2d3e-4f50-4617-8293-a4b5c6d7e8f9")
	assert.True(t, errors.Is(err, ErrTerminalGone), "err = %v", err)
}

// #3268: a background job left in the shell after the agent CLI exits ('sleep &') is
// a child of the shell, but nothing is running in the foreground: the shell is still
// at its prompt, so the agent has exited and nothing may be typed into it.
func TestAgentShellIdleIgnoresBackgroundJobs(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const sid = "5c7e9a21-3b4d-4f60-8a1b-9d2e3f4a5b60"
	name := "claude-" + sid
	createTestSession(t, c, name, t.TempDir())
	state := func() (bool, bool) {
		v, _, err := c.AgentShellIdle(ctx, name, "claude", sid)
		return v, err == nil
	}
	require.Eventually(t, func() bool { v, ok := state(); return ok && v }, 5*time.Second, 25*time.Millisecond)

	// A job in the background: the shell is back at its prompt.
	require.NoError(t, c.SendKeysToTarget(ctx, name+".0", "sleep 60 &"))
	require.Eventually(t, func() bool {
		out, _ := c.DisplayMessage(ctx, name+".0", "#{pane_pid}")
		return out != ""
	}, 5*time.Second, 25*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	idle, ok := state()
	require.True(t, ok)
	assert.True(t, idle, "a background job is not the agent: the shell is idle")

	// A foreground command on top of the background job is still busy.
	require.NoError(t, c.SendKeysToTarget(ctx, name+".0", "sleep 30"))
	require.Eventually(t, func() bool { v, ok := state(); return ok && !v }, 5*time.Second, 25*time.Millisecond, "a foreground command keeps the shell busy")
	require.NoError(t, c.SendRawKeys(ctx, name, []string{"C-c"}, "claude", sid))
	require.Eventually(t, func() bool { v, ok := state(); return ok && v }, 5*time.Second, 25*time.Millisecond)
}
