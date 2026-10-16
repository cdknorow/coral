package routes

import (
	"net/http"
	"sync"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
)

// What the dashboard needs to know about an open prompt rides on the chat poll's reply (#3720): the pending
// tool call, always, and the options of a prompt open on the terminal, only when one may be open. They used to
// be two more requests a second per open agent (GET /pending-tool and GET /prompt-options, the second reading a
// pane capture, 11 to 26 ms), and both routes stay for dashboards that still ask. A reply without a
// "pending" key is from a server that predates this; the client then asks the old routes.

// promptHints holds, per session, whether its latest state-changing hook event leaves a prompt open. A session
// with no such event since this server started is unknown, and unknown means "may be open": the options are
// then read as before, so an agent without hooks, or a restarted server, costs what it did.
type promptHints struct {
	mu sync.Mutex
	m  map[string]bool // session_id -> a request to the user is open
}

// Enough for every live session many times over; past it the table is dropped, which only turns every
// session back to unknown.
const promptHintsMax = 4096

// note records an event. Only the events that open or close a request change the answer: an unrelated one
// (a goal, a file change) must not hide a prompt that is still open.
func (p *promptHints) note(sessionID, eventType string) {
	var waiting bool
	switch eventType {
	case "permission_request", "notification":
		waiting = true
	case "stop", "prompt_submit", "request_resolved", "tool_use", "session_reset":
		waiting = false
	case "session_end":
		p.mu.Lock()
		delete(p.m, sessionID)
		p.mu.Unlock()
		return
	default:
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil || len(p.m) >= promptHintsMax {
		p.m = map[string]bool{}
	}
	p.m[sessionID] = waiting
}

// mayBeOpen reports whether a prompt may be open on the session's terminal.
func (p *promptHints) mayBeOpen(sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	waiting, known := p.m[sessionID]
	return !known || waiting
}

// readsPromptsFromScreen: these agents report no hook for their sign-in, update or approval prompts, so their
// screen is read on every poll (the same list the dashboard used).
func readsPromptsFromScreen(agentType string) bool {
	switch agentType {
	case at.Codex, at.Agy, at.Antigravity, at.Gemini:
		return true
	}
	return false
}

// addNeedsInput adds "pending" and, when a prompt may be open, "prompt_options" to a chat reply. It needs the
// session's id: a poll by name alone gets neither (and the client asks the old routes).
func (h *SessionsHandler) addNeedsInput(r *http.Request, reply map[string]any, name, agentType, sessionID string) {
	// An older page ("load earlier messages") has no use for either.
	if sessionID == "" || r.URL.Query().Get("before") != "" {
		return
	}
	var pending any
	t, hasPending := h.pending.get(sessionID, time.Now())
	if hasPending {
		pending = t
	}
	reply["pending"] = pending
	if !hasPending && !readsPromptsFromScreen(agentType) && !h.hints.mayBeOpen(sessionID) {
		return
	}
	// The same check GET /prompt-options makes: the pane read is the exact target's, never another agent's.
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, _ := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, false); status != 0 {
			return
		}
	}
	screen, ok := h.capturePromptScreen(r, name, agentType, sessionID)
	if !ok {
		screen = promptScreen{Options: []promptOption{}}
	}
	reply["prompt_options"] = screen
}
