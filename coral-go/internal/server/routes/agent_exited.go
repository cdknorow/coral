package routes

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/cdknorow/coral/internal/ptymanager"
)

// errAgentExited is returned for automated input to an agent whose CLI has
// exited and left its terminal at a plain shell prompt (#3219).
var errAgentExited = errors.New("the agent has exited; its terminal is a plain shell, so nothing was typed")

// exitLogged holds the sessions whose skipped automated input was already logged.
var exitLogged sync.Map

// automatedInputBlocked reports whether automated input (board notifications and
// nudges, agent UI notifications, auto-accept) must not be typed into an agent's
// terminal because its CLI has exited: a shell would run the text as a command
// (#3219). It logs once per exit. It is never used for input the user types in
// the dashboard, and never blocks a 'terminal' agent, whose shell is the agent.
func automatedInputBlocked(ctx context.Context, term any, name, agentType, sessionID, what string) bool {
	key := name
	if sessionID != "" {
		key = sessionID
	}
	exited, fg := ptymanager.AgentExited(ctx, term, name, agentType, sessionID)
	if !exited {
		exitLogged.Delete(key)
		return false
	}
	if _, already := exitLogged.LoadOrStore(key, true); !already {
		slog.Warn("agent CLI has exited; not typing into its shell", "session", name, "foreground", fg, "skipped", what)
	}
	return true
}

// sendAutomated types board-generated text (a notification or nudge) into an
// agent's terminal, unless its CLI has exited (#3219).
func (h *BoardHandler) sendAutomated(ctx context.Context, sessionName, text string) error {
	if automatedInputBlocked(ctx, h.terminal, sessionName, "", "", "board message") {
		return errAgentExited
	}
	return h.terminal.SendInput(ctx, sessionName, text, "", "")
}
