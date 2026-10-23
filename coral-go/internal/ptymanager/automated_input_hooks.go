// The callbacks the input gate reports through (the needs-input check, held too long, gave up, ended) and the notifier that runs them off the gate's lock (moved out of automated_input.go, #5091; no behaviour change).

package ptymanager

import (
	"context"
	"sync"

	"github.com/subgentic/coral/internal/safego"
)

// NeedsInputFunc reports whether the agent's state, derived from its hook and
// transcript events, says it is waiting on the user. The server sets it at
// startup; nil means only the screen is checked.
var needsInputFunc struct {
	sync.RWMutex
	fn func(ctx context.Context, name, agentType, sessionID string) bool
}

// SetNeedsInputFunc installs the state check used by the gate.
func SetNeedsInputFunc(fn func(ctx context.Context, name, agentType, sessionID string) bool) {
	needsInputFunc.Lock()
	needsInputFunc.fn = fn
	needsInputFunc.Unlock()
}

// HeldTooLongFunc is called once when a send has been held a while (2 minutes), so
// the dashboard can show that the agent needs the user (instead of anything
// being typed into the prompt). kind says what holds it when it is a request to the
// user: the agent's own Blocked kind (permission, question, plan, elicitation),
// "trust" for a trust screen at launch, or "prompt" for one read from the screen or a
// state of needs input; "" for the input box (a draft in it, no box on screen, a
// paste that never landed) or a busy agent, which is not a request (#3468, #3482).
var heldTooLong struct {
	sync.RWMutex
	fn func(name, agentType, sessionID, what, kind string)
}

// HeldEndedFunc is called once when a session that had a send reported to
// HeldTooLongFunc has no reported send held any more: delivered, dropped (DropHeld)
// or expired. So the notice can be cleared on agents with no hook events to clear it
// (agy, codex; #3471). It and HeldTooLongFunc are called in order, outside the gate's
// lock.
var heldEnded struct {
	sync.RWMutex
	fn func(name, agentType, sessionID string)
}

// HeldGaveUpFunc is called when the gate gives up a held paste of its own (#3708): the
// text was pasted, but its input box then never showed it, couldn't be read, or showed
// text Coral can't place, for pasteStuckAfter. The send is dropped (never left to block
// later sends) and the text may still be in the agent's input box, so the dashboard says
// so. why is the reason; called outside the gate's lock, in order with the other notices.
var heldGaveUp struct {
	sync.RWMutex
	fn func(name, agentType, sessionID, what, why string)
}

// SetHeldGaveUpFunc installs the callback for a held paste given up.
func SetHeldGaveUpFunc(fn func(name, agentType, sessionID, what, why string)) {
	heldGaveUp.Lock()
	heldGaveUp.fn = fn
	heldGaveUp.Unlock()
}

// SetHeldEndedFunc installs the callback for a session's reported sends all gone.
func SetHeldEndedFunc(fn func(name, agentType, sessionID string)) {
	heldEnded.Lock()
	heldEnded.fn = fn
	heldEnded.Unlock()
}

// heldDropped holds the callbacks OnHeldDropped installed.
var heldDropped struct {
	sync.Mutex
	next int
	fns  map[int]func(name, why string)
}

// OnHeldDropped installs fn to be called, outside the gate's lock, each time DropHeld discards what
// a session had held: its agent is killed, spawned, restarted or launched again in that terminal.
// The nudges and messages Coral sent that process, held or typed, are gone with it, so a caller
// that tracks "already told this agent" starts over (#6544). fn must not block. The returned
// function removes it.
func OnHeldDropped(fn func(name, why string)) (remove func()) {
	heldDropped.Lock()
	defer heldDropped.Unlock()
	if heldDropped.fns == nil {
		heldDropped.fns = make(map[int]func(name, why string))
	}
	id := heldDropped.next
	heldDropped.next++
	heldDropped.fns[id] = fn
	return func() {
		heldDropped.Lock()
		delete(heldDropped.fns, id)
		heldDropped.Unlock()
	}
}

func callHeldDropped(name, why string) {
	heldDropped.Lock()
	fns := make([]func(name, why string), 0, len(heldDropped.fns))
	for _, fn := range heldDropped.fns {
		fns = append(fns, fn)
	}
	heldDropped.Unlock()
	for _, fn := range fns {
		fn(name, why)
	}
}

// notifier runs callbacks one at a time, in the order posted, off the caller's
// goroutine: a held notice and its end must never arrive the wrong way round.
type notifier struct {
	mu      sync.Mutex
	q       []func()
	running bool
}

func (n *notifier) post(f func()) {
	n.mu.Lock()
	n.q = append(n.q, f)
	if !n.running {
		n.running = true
		go func() { defer safego.Recover("ptymanager/automated_input:n.run"); n.run() }()
	}
	n.mu.Unlock()
}

func (n *notifier) run() {
	for {
		n.mu.Lock()
		if len(n.q) == 0 {
			n.running = false
			n.mu.Unlock()
			return
		}
		f := n.q[0]
		n.q[0] = nil
		n.q = n.q[1:]
		n.mu.Unlock()
		// One notice's panic must not stop the queue: running would stay true and no later
		// notice would ever be sent (#3998).
		func() {
			defer safego.Recover("automated input: a notice")
			f()
		}()
	}
}

// SetHeldTooLongFunc installs the callback for sends held a while.
func SetHeldTooLongFunc(fn func(name, agentType, sessionID, what, kind string)) {
	heldTooLong.Lock()
	heldTooLong.fn = fn
	heldTooLong.Unlock()
}
