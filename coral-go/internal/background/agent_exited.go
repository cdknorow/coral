package background

import (
	"context"
	"log/slog"
	"sync"

	"github.com/cdknorow/coral/internal/ptymanager"
)

// exitLogged holds the sessions whose 'agent exited' skip was already logged,
// so a nudge loop logs once per exit rather than every few minutes.
var exitLogged sync.Map

// terminalLogged holds the terminal agents whose skipped input was already logged.
var terminalLogged sync.Map

// skipIfAgentExited reports whether automated input for an agent's terminal must
// be skipped because its CLI has exited and left a plain shell there (#3219): a
// shell would run the text as a command. It logs once per exit.
func skipIfAgentExited(ctx context.Context, rt any, logger *slog.Logger, name, what string) bool {
	// A terminal agent's terminal is a shell: automated text is never typed there (#3376).
	if ptymanager.IsTerminalAgent(name, "") {
		if _, already := terminalLogged.LoadOrStore(name, true); !already && logger != nil {
			logger.Info("not typing automated input into a terminal agent's shell", "session", name, "skipped", what)
		}
		return true
	}
	exited, fg := ptymanager.AgentExited(ctx, rt, name, "", "")
	if !exited {
		exitLogged.Delete(name)
		return false
	}
	if _, already := exitLogged.LoadOrStore(name, true); !already && logger != nil {
		logger.Warn("agent CLI has exited; not typing into its shell", "session", name, "foreground", fg, "skipped", what)
	}
	return true
}
