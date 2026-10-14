package ptymanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/internal/tmux"
)

// fgTerm's shell is idle when cmd is a shell name (the CLI exited), busy otherwise.
type fgTerm struct{ cmd string }

func (f fgTerm) AgentShellIdle(context.Context, string, string, string) (bool, string, bool) {
	if f.cmd == "" {
		return false, "", false
	}
	return tmux.IsShellCommand(f.cmd), f.cmd, true
}

const exitedSID = "5f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"

// #3219: a pane showing a plain shell means the agent CLI has exited.
func TestAgentExited(t *testing.T) {
	ctx := context.Background()
	name := "claude-" + exitedSID

	exited, fg := AgentExited(ctx, fgTerm{"zsh"}, name, "claude", exitedSID)
	assert.True(t, exited)
	assert.Equal(t, "zsh", fg)

	exited, _ = AgentExited(ctx, fgTerm{"node"}, name, "claude", exitedSID)
	assert.False(t, exited, "the agent CLI is running")

	// The agent type comes from an exact session name when the caller has none (board callers).
	exited, _ = AgentExited(ctx, fgTerm{"bash"}, name, "", "")
	assert.True(t, exited)

	// Unknown: the backend cannot name the foreground process, or does not implement it.
	exited, _ = AgentExited(ctx, fgTerm{""}, name, "claude", exitedSID)
	assert.False(t, exited)
	exited, _ = AgentExited(ctx, struct{}{}, name, "claude", exitedSID)
	assert.False(t, exited)
}

// A 'terminal' agent's shell IS the agent: never treated as exited (Orchestrator's must).
func TestAgentExitedNeverForATerminalAgent(t *testing.T) {
	ctx := context.Background()
	exited, _ := AgentExited(ctx, fgTerm{"zsh"}, "terminal-"+exitedSID, "terminal", exitedSID)
	assert.False(t, exited)
	exited, _ = AgentExited(ctx, fgTerm{"zsh"}, "terminal-"+exitedSID, "", "")
	assert.False(t, exited, "also when the type comes from the session name")
}
