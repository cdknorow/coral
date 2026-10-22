// Whether an agent's terminal is waiting on the user: the screen and the hook-driven check (moved out of automated_input.go, #5091; no behaviour change).

package ptymanager

import (
	"context"
	"strings"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// WaitingOnUser reports whether the agent is waiting on a prompt from the user,
// and why. An open prompt on its screen always counts. Its state (hook and
// transcript events, NeedsInputFunc) counts too, unless the screen positively
// shows the agent's normal input box: a missed hook must not hold every
// notification until the next event clears the state. With no screen to read,
// the state decides.
func WaitingOnUser(ctx context.Context, term any, name, agentType, sessionID string) (bool, string) {
	needsInputFunc.RLock()
	fn := needsInputFunc.fn
	needsInputFunc.RUnlock()
	stateWaiting := fn != nil && fn(ctx, name, agentType, sessionID)
	if r, ok := term.(ScreenReader); ok {
		if screen, err := r.AgentScreen(ctx, name, agentType, sessionID); err == nil {
			if open, why := PromptOnScreen(screen); open {
				return true, why
			}
			if stateWaiting && !InputBoxOnScreen(screen) {
				return true, "state: needs input"
			}
			return false, ""
		}
	}
	if stateWaiting {
		return true, "state: needs input"
	}
	return false, ""
}

// waiting reports whether this send must wait, why, and whether it is a request to
// the user. It runs the screen rule and, where the session has signals, the signal
// rule (#3482). The decision follows the screen rule, with the signal rule logged next to
// it (shadow mode), unless the gate follows the signals for this agent type
// (gateFollowsSignals, #3578). Then the signal rule decides, except that a send it would
// hold only for the turn goes by the screen rule (today's behaviour) when it is a mention,
// or when the turn has run for inTurnFallback: a mention isn't kept from a working agent,
// and nothing waits on a turn for ever. The screen rule still holds either for a prompt
// or a menu.
func (in AutomatedInput) waiting(ctx context.Context) (waiting bool, why, kind string) {
	waiting, why, kind = in.screenWaiting(ctx)
	sWait, sWhy, sKind, st, ok := in.signalDecision(ctx)
	if !ok {
		return waiting, why, kind
	}
	agentType, _ := in.identity()
	decided := "screen"
	if gateFollowsSignals(agentType) {
		inTurnHold := sWait && st.State == StateInTurn
		// The fallback counts from the turn's start; when the signals don't carry it, from
		// this send's first hold, so a missing Since never holds a send for ever (Backend's
		// NIT). A send not yet held has nothing to count from and is held now.
		since := st.Since
		if since.IsZero() {
			since = in.heldSince
		}
		switch {
		case inTurnHold && in.Mention:
			decided = "screen: a mention goes mid-turn"
		case inTurnHold && !since.IsZero() && gate.now().Sub(since) >= inTurnFallback:
			decided = "screen: in turn over " + inTurnFallback.String()
		default:
			decided = "signals"
		}
	}
	if agentType == at.Codex && sWait && st.State == StateInTurn && (st.Since.IsZero() || gate.now().Sub(st.Since) < inTurnFallback) {
		// Codex keeps what is typed during its turn, so its own signals hold every send then, a mention too, whatever the screen
		// shows (#247: while it streams, the rendered screen shows neither its working line nor its box).
		decided = "signals: Codex in turn"
	}
	gate.shadowLog(in, waiting, why, sWait, sWhy, sKind, st, decided)
	if strings.HasPrefix(decided, "signals") {
		return sWait, sWhy, sKind
	}
	return waiting, why, kind
}

// screenWaiting is the screen rule: the agent is waiting on the user (prompt), or
// (RequireInputBox) its input box isn't on screen yet.
func (in AutomatedInput) screenWaiting(ctx context.Context) (waiting bool, why, kind string) {
	if waiting, why := WaitingOnUser(ctx, in.Term, in.Name, in.AgentType, in.SessionID); waiting {
		return true, why, promptKind(why)
	}
	if agentType, sessionID := in.identity(); agentType == at.Codex {
		// Codex keeps what is typed while it works (in its box, or queued), so nothing is typed into a working Codex,
		// mention or not: Coral's text left there was taken for a person's draft and held everything behind it (#247).
		if r, ok := in.Term.(ScreenReader); ok {
			if screen, err := r.AgentScreen(ctx, in.Name, agentType, sessionID); err == nil {
				if line, busy := codexWorkingOnScreen(screen); busy {
					return true, busyWhy + line, ""
				}
			}
		}
	}
	if !in.RequireInputBox {
		return false, "", ""
	}
	r, ok := in.Term.(ScreenReader)
	if !ok {
		return false, "", "" // can't tell: the caller's own startup wait applies
	}
	screen, err := r.AgentScreen(ctx, in.Name, in.AgentType, in.SessionID)
	if err != nil || !InputBoxOnScreen(screen) {
		return true, "the agent's input box isn't on screen yet", ""
	}
	return false, "", ""
}
