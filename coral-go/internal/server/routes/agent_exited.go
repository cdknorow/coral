package routes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/logparse"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/terminalinput"
)

// errAgentExited is returned for automated input to an agent whose CLI has
// exited and left its terminal at a plain shell prompt (#3219).
var errAgentExited = errors.New("the agent has exited; its terminal is a plain shell, so nothing was typed")

// exitLogged holds the sessions whose skipped automated input was already logged.
var exitLogged sync.Map

// terminalLogged holds the terminal agents whose skipped automated input was logged.
var terminalLogged sync.Map

// refusedNotices keeps a refused board notice's log line to once per board and session
// ("<board>\x00<session name>").
var refusedNotices sync.Map

// All three are forgotten when the session ends (#4057): they are keyed by the session's
// name or id, and would otherwise keep every session the server has run.
func init() {
	ptymanager.OnSessionEnd(func(name string) {
		exitLogged.Delete(name)
		terminalLogged.Delete(name)
		if sid := naming.SessionIDFromName(name); sid != "" {
			exitLogged.Delete(sid)
			terminalLogged.Delete(sid)
		}
		refusedNotices.Range(func(k, _ any) bool {
			if _, session, _ := strings.Cut(k.(string), "\x00"); session == name {
				refusedNotices.Delete(k)
			}
			return true
		})
	})
}

// automatedInputBlocked reports whether automated input (board notifications and
// nudges, agent UI notifications, auto-accept) must not be typed into an agent's
// terminal because its CLI has exited: a shell would run the text as a command
// (#3219). It logs once per exit. It is never used for input the user types in
// the dashboard. A 'terminal' agent, whose terminal is a shell, never gets automated
// input at all (#3376).
func automatedInputBlocked(ctx context.Context, term any, name, agentType, sessionID, what string) bool {
	key := name
	if sessionID != "" {
		key = sessionID
	}
	// A terminal agent's terminal is a shell: automated text is never typed there (#3376).
	if ptymanager.IsTerminalAgent(name, agentType) {
		if _, already := terminalLogged.LoadOrStore(key, true); !already {
			slog.Info("not typing automated input into a terminal agent's shell", "session", name, "skipped", what)
		}
		return true
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
// agent's terminal, unless its CLI has exited (#3219). Always as one line: a newline in
// a name or preview would be an Enter keystroke (#3376). It goes through the
// automated-input gate: held while the agent waits on a prompt (#3431).
//
// project is the board the text is about. It is typed only into a session subscribed on that
// board, and only into exactly that session: its agent type and id go to the terminal, which
// then never resolves the name another way (#3864).
func (h *BoardHandler) sendAutomated(ctx context.Context, project, sessionName, text string) error {
	agentType, sessionID, err := h.noticeTarget(ctx, project, sessionName)
	if err != nil {
		if _, seen := refusedNotices.LoadOrStore(project+"\x00"+sessionName, true); !seen {
			slog.Warn("board notice refused", "board", project, "session", sessionName, "error", err)
		}
		return err
	}
	return deliverAutomated(ctx, h.terminal, sessionName, agentType, sessionID, "", board.OneLine(text), "board message")
}

// errNoticeTarget is why a board notice was not typed into a session.
var errNoticeTarget = errors.New("not this board's agent session")

// noticeTarget is the exact session a notice about project may be typed into: sessionName
// must be an agent session ("<type>-<id>"), subscribed on project, and live as that type.
// Anything else, or a failed lookup, refuses.
func (h *BoardHandler) noticeTarget(ctx context.Context, project, sessionName string) (string, string, error) {
	agentType, sessionID := logparse.ParseSessionName(sessionName)
	if sessionID == "" || h.bs == nil {
		return "", "", fmt.Errorf("%w: %q is not an agent session", errNoticeTarget, sessionName)
	}
	on, err := h.bs.SessionSubscribedOn(ctx, project, sessionName)
	if err != nil {
		return "", "", err
	}
	if !on {
		return "", "", fmt.Errorf("%w: the session is not subscribed on this board", errNoticeTarget)
	}
	if h.liveSessionType != nil {
		liveType, ok, err := h.liveSessionType(ctx, sessionID)
		if err != nil {
			return "", "", err
		}
		if !ok || !strings.EqualFold(liveType, agentType) {
			return "", "", fmt.Errorf("%w: no live %s session with that id", errNoticeTarget, agentType)
		}
	}
	return agentType, sessionID, nil
}

// deliverAutomated is how every automated sender in this package types into an
// agent (#3431): not into an exited agent's shell or a terminal agent
// (automatedInputBlocked), and only when the agent isn't waiting on a prompt
// (ptymanager.DeliverAutomated holds it until the prompt is gone). key groups
// sends that supersede each other ("" for the text itself).
func deliverAutomated(ctx context.Context, term ptymanager.SessionTerminal, name, agentType, sessionID, key, text, what string) error {
	return deliverAutomatedWhile(ctx, term, name, agentType, sessionID, key, text, what, nil)
}

func deliverAutomatedWhile(ctx context.Context, term ptymanager.SessionTerminal, name, agentType, sessionID, key, text, what string, wanted func(context.Context) error) error {
	check := func(ctx context.Context) error {
		if wanted != nil {
			if err := wanted(ctx); err != nil {
				return err
			}
		}
		if automatedInputBlocked(ctx, term, name, agentType, sessionID, what) {
			return errAgentExited
		}
		return nil
	}
	if err := check(ctx); err != nil {
		return err
	}
	_, err := ptymanager.DeliverAutomated(ctx, ptymanager.AutomatedInput{
		Term: term, Name: name, AgentType: agentType, SessionID: sessionID,
		Key: key, Text: text, What: what,
		// As a bracketed paste where the agent takes one: a menu ignores it (#3457).
		Paste: true,
		Ready: check,
		Send: func(ctx context.Context) error {
			// Re-checked at send time too: a held send may be typed much later.
			if err := check(ctx); err != nil {
				return err
			}
			return term.SendInput(ctx, terminalinput.Coral, name, text, agentType, sessionID) // Coral's own text
		},
	})
	return err
}

// sendAutomatedHTTP sends input an automated HTTP caller asked for, through the
// gate. When it is held it answers 202 itself and reports true; on an error it
// answers it and reports true; when it was typed it reports false and the
// handler answers as usual.
func (h *SessionsHandler) sendAutomatedHTTP(w http.ResponseWriter, r *http.Request, name, agentType, sessionID, text, what string, send func(context.Context) error) bool {
	delivery, err := ptymanager.DeliverAutomated(r.Context(), ptymanager.AutomatedInput{
		Term: h.terminal, Name: name, AgentType: agentType, SessionID: sessionID,
		Text: text, What: "automated " + what, Send: send,
	})
	if err != nil {
		writeTerminalError(w, err)
		return true
	}
	if delivery == ptymanager.Held {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "held": true,
			"note": "the agent is waiting on a prompt for the user; this input is typed once that prompt is answered"})
		return true
	}
	return false
}
