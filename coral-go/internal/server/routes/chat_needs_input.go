package routes

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// What the dashboard needs to know about an open prompt rides on the chat poll's reply (#3720): the pending
// tool call, always, and the options of a prompt open on the terminal, only when one may be open. They used to
// be two more requests a second per open agent (GET /pending-tool and GET /prompt-options, the second reading a
// pane capture, 11 to 26 ms). Those two routes are gone: the dashboard and the server ship together.

// promptHints holds, per session, whether its latest state-changing hook event leaves a prompt open. A session
// with no such event since this server started is unknown, and unknown means "may be open": the options are
// then read as before, so an agent without hooks, or a restarted server, costs what it did.
//
// A read of the options answers the session's chat polls for promptReadTTL (#3745): an agent whose hook state
// is unknown, or that has no prompt hooks, was read on every poll (a pane capture each time, the chat p95 rise
// in #3740), and the popout's poll doubled it. One read now serves every poll in that window, and a hook event
// or a pending tool for the session, or an answer to its prompt, drops it, so a prompt that opens shows on the
// next poll.
type promptHints struct {
	mu    sync.Mutex
	m     map[string]bool        // session_id -> a request to the user is open
	slash map[string]time.Time   // session_id -> when a slash command was last typed into it by the Operator (#5488)
	reads map[string]*promptRead // exact target (session_id, name, agent type) -> its latest read
	ttl   time.Duration          // how long one read answers its polls; 0: defaultPromptReadTTL (tests set their own, #6991)
}

// defaultPromptReadTTL is how long one read of a session's prompt options answers its polls (promptHints.ttl replaces it in a test).
const defaultPromptReadTTL = 2 * time.Second

func (p *promptHints) readTTL() time.Duration {
	if p.ttl > 0 {
		return p.ttl
	}
	return defaultPromptReadTTL
}

// promptReadTimeout bounds a shared read. It runs on its own context, not the first poller's request: a
// client that goes away mid-read must not leave the others an empty list for promptReadTTL (Security on
// #3745).
var promptReadTimeout = 5 * time.Second

// promptRead is one read of a session's prompt options; done closes when it is complete.
type promptRead struct {
	at     time.Time
	done   chan struct{}
	screen promptScreen
	shown  bool // the reply carries the options (the exact target checked out)
}

func promptReadKey(sessionID, name, agentType string) string {
	return sessionID + "\x00" + name + "\x00" + agentType
}

// read returns the session's prompt options: those of a read made in the last promptReadTTL, or a new one from
// fetch. One read at a time per target: a poll that comes while one is under way waits for it.
func (p *promptHints) read(key string, now time.Time, fetch func() (promptScreen, bool)) (promptScreen, bool) {
	p.mu.Lock()
	if e, ok := p.reads[key]; ok && now.Sub(e.at) < p.readTTL() {
		p.mu.Unlock()
		<-e.done
		return e.screen, e.shown
	}
	if p.reads == nil || len(p.reads) >= promptHintsMax {
		p.reads = map[string]*promptRead{}
	}
	e := &promptRead{at: now, done: make(chan struct{})}
	p.reads[key] = e
	p.mu.Unlock()
	defer close(e.done)
	e.screen, e.shown = fetch()
	return e.screen, e.shown
}

// forget drops the session's reads: what its terminal shows may have changed.
func (p *promptHints) forget(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forgetLocked(sessionID)
}

func (p *promptHints) forgetLocked(sessionID string) {
	prefix := sessionID + "\x00"
	for k := range p.reads {
		if strings.HasPrefix(k, prefix) {
			delete(p.reads, k)
		}
	}
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
		p.forgetLocked(sessionID)
		p.mu.Unlock()
		return
	default:
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forgetLocked(sessionID)
	if p.m == nil || len(p.m) >= promptHintsMax {
		p.m = map[string]bool{}
	}
	p.m[sessionID] = waiting
}

// slashWindow is how long after the Operator types a slash command into an agent its screen is read for a prompt (a var for tests). Claude Code answers some of them ("/model", "/effort") with a
// confirmation dialog ("Switch model?", "Change effort level?") that sends no hook event, so no hint says a prompt is open and the dialog never reached the chat (#5488).
var slashWindow = time.Minute

// noteSlash records that the Operator typed a slash command into the session: for slashWindow its prompt may be open, whatever its last hook event said.
func (p *promptHints) noteSlash(sessionID string) {
	if sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.slash == nil || len(p.slash) >= promptHintsMax {
		p.slash = map[string]time.Time{}
	}
	p.slash[sessionID] = time.Now()
	p.forgetLocked(sessionID) // what it shows may be about to change: the next poll reads it afresh
}

// mayBeOpen reports whether a prompt may be open on the session's terminal.
func (p *promptHints) mayBeOpen(sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if at, ok := p.slash[sessionID]; ok {
		if time.Since(at) < slashWindow {
			return true
		}
		delete(p.slash, sessionID)
	}
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
	// One read per target per promptReadTTL, keyed on the exact target it checked, so a poll naming another
	// terminal never gets this one's screen.
	screen, shown := h.hints.read(promptReadKey(sessionID, name, agentType), time.Now(), func() (promptScreen, bool) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), promptReadTimeout)
		defer cancel()
		// The check GET /prompt-options used to make: the pane read is the exact target's, never another agent's.
		if shouldValidateExactTarget(agentType, sessionID) {
			if _, status, _ := h.validateExactLiveTarget(ctx, name, agentType, sessionID, false); status != 0 {
				return promptScreen{}, false
			}
		}
		s, ok := h.capturePromptScreen(ctx, name, agentType, sessionID)
		if !ok {
			s = promptScreen{Options: []promptOption{}}
		}
		return s, true
	})
	if shown {
		reply["prompt_options"] = screen
	}
}
