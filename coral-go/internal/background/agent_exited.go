package background

import (
	"context"
	"errors"
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

// deliverAutomated is how every automated sender in this package types into an
// agent (#3431): never into an exited agent's shell or a terminal agent
// (skipIfAgentExited, which reports it as skipped), and only when the agent isn't
// waiting on a prompt: ptymanager.DeliverAutomated holds it until the prompt is
// gone. key groups sends that supersede each other ("" for the text itself);
// requireInputBox holds it until the agent's normal input box is on screen (a
// launch's first prompt).
func deliverAutomated(ctx context.Context, rt AgentRuntime, logger *slog.Logger, name, agentType, sessionID, key, text, what string, requireInputBox bool) (skipped bool, err error) {
	if skipIfAgentExited(ctx, rt, logger, name, what) {
		return true, nil
	}
	_, err = ptymanager.DeliverAutomated(ctx, ptymanager.AutomatedInput{
		Term: rt, Name: name, AgentType: agentType, SessionID: sessionID,
		Key: key, Text: text, What: what, RequireInputBox: requireInputBox,
		// As a bracketed paste where the agent takes one: a menu ignores it (#3457).
		Paste: true,
		Ready: func(ctx context.Context) error {
			if skipIfAgentExited(ctx, rt, logger, name, what) {
				return errSkippedExited
			}
			return nil
		},
		Send: func(ctx context.Context) error {
			// Re-checked at send time too: a held send may be typed much later.
			if skipIfAgentExited(ctx, rt, logger, name, what) {
				return nil
			}
			return rt.SendInput(ctx, name, text)
		},
	})
	if errors.Is(err, errSkippedExited) {
		return true, nil // the agent exited before its turn: skipped, as above
	}
	return false, err
}

// errSkippedExited: the agent's CLI exited before a held send's turn; nothing typed.
var errSkippedExited = errors.New("skipped: the agent has exited")
