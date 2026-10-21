package ptymanager

import (
	"context"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/logparse"
)

// ShellIdleChecker is implemented by terminals that can tell whether an agent's
// own shell is idle, with nothing (no agent CLI) running in it (#3219).
type ShellIdleChecker interface {
	AgentShellIdle(ctx context.Context, name, agentType, sessionID string) (idle bool, shell string, ok bool)
}

// IsTerminalAgent reports whether a session is a 'terminal' agent: its terminal IS a
// shell, so automated text (a notification naming a sender, a message preview, a task
// title) must never be typed there. Quoting can't make arbitrary text safe for a shell:
// $( ) and backticks expand inside double quotes (#3376). agentType "" is read from the
// session name.
func IsTerminalAgent(name, agentType string) bool {
	if agentType == "" {
		agentType, _ = logparse.ParseSessionName(name)
	}
	return agentType == at.Terminal
}

// AgentExited reports whether an agent's CLI has exited and left its terminal at
// a plain shell prompt, so automated input (nudges, notifications, scheduled
// prompts, auto-accept) must not be typed there: a shell would run it as a
// command (#3219). It is false when it cannot tell (the terminal is gone, the
// backend or platform cannot say), and always false for a 'terminal' agent,
// whose shell is the agent. Input the user types in the dashboard is never
// checked.
func AgentExited(ctx context.Context, term any, name, agentType, sessionID string) (bool, string) {
	if agentType == "" {
		agentType, _ = logparse.ParseSessionName(name)
	}
	if agentType == at.Terminal {
		return false, ""
	}
	c, ok := term.(ShellIdleChecker)
	if !ok {
		return false, ""
	}
	idle, shell, ok := c.AgentShellIdle(ctx, name, agentType, sessionID)
	if !ok {
		return false, ""
	}
	return idle, shell
}

// AgentShellIdle implements ShellIdleChecker for tmux.
func (t *TmuxSessionTerminal) AgentShellIdle(ctx context.Context, name, agentType, sessionID string) (bool, string, bool) {
	idle, shell, err := t.client.AgentShellIdle(ctx, name, agentType, sessionID)
	if err != nil {
		return false, "", false
	}
	return idle, shell, true
}

// AgentShellIdle implements ShellIdleChecker for the PTY backend.
func (p *PTYSessionTerminal) AgentShellIdle(_ context.Context, name, _, sessionID string) (bool, string, bool) {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return false, "", false
	}
	return p.backend.ShellIdle(key)
}
