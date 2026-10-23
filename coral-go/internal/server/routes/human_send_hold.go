package routes

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/pkg/tunnel"
)

// A person's chat message is not typed into a box that asks the person something (#6610). When an agent shows a permission or question box (a plan menu, an elicitation) its own
// signals say so: its hook events make the state Blocked (gateState). A chat message typed then goes into the box as its answer, or interrupts it. So the message is held while
// the agent's signals say it waits on a box, through every box in a row: it is typed only once the agent has been clear of boxes for personHoldSettle (a second box that opens
// right after the first starts the wait again). Signals only: an agent with no signal, or a box that is on the screen and not in the signals, keeps the old behaviour (a person may
// answer a menu by typing its digit). Held messages go out in the order they were sent; a held message is not lost when the person leaves the page, and it is dropped, with a
// refusal, when the agent's session ends or the box stays open personHoldMax.

var (
	// personHoldSettle is how long the agent must show no box before a held message is typed: a next box opens within moments of the last one's answer.
	personHoldSettle = 1500 * time.Millisecond
	// personHoldPoll is how often the agent's signals are read while a message waits.
	personHoldPoll = 250 * time.Millisecond
	// personHoldMax is how long a message waits for the boxes to clear.
	personHoldMax = 30 * time.Minute
	// personHoldMaxPerAgent and personHoldMaxTotal bound what can wait.
	personHoldMaxPerAgent = 8
	personHoldMaxTotal    = 64
)

var errSendModeLowered = &errHumanSend{"hub_mode_lowered", "This computer's Coral Hub access was lowered to view-only while your message waited, so it was not sent."}

var errSendHeldExpired = &errHumanSend{"held_expired", "The agent's question was still open after 30 minutes, so your message was not sent. Answer it, then send your message again."}

// personHeld is a person's message waiting for an agent's boxes.
type personHeld struct {
	name, agentType, sessionID, text string
	done                             func(error) // called once, with how it ended (nil: typed and submitted); may be nil
	viaHub                           bool        // the message came through Coral Hub: it is typed only while this computer's mode still allows Interact
}

// personHolds is the messages waiting, per agent (key: its session id, else its terminal name). Its zero value is ready.
type personHolds struct {
	mu      sync.Mutex
	queues  map[string][]*personHeld
	running map[string]bool
	// releasing is set while the agent's head message has been taken off the queue and is waiting its turn or being typed.
	releasing map[string]bool
	total     int
}

func personKey(name, sessionID string) string {
	if sessionID != "" {
		return sessionID
	}
	return name
}

// blockedOnBox reports whether the agent's own signals say it waits on a box that asks the person. No signal (ok false) is not a box.
func (h *SessionsHandler) blockedOnBox(ctx context.Context, name, agentType, sessionID string) bool {
	st, ok := h.gateState(ctx, name, agentType, sessionID)
	return ok && st.State == ptymanager.StateBlocked
}

// deliverPersonSend is deliverHumanSend for a person's chat message, held while the agent waits on a box. held is true when the message was queued: it is typed later, and done (which
// may be nil) is called then with how that ended. When it is not held the message was delivered here, and done is not used.
func (h *SessionsHandler) deliverPersonSend(ctx context.Context, name, agentType, sessionID, text string, viaHub bool, done func(error)) (held bool, err error) {
	key := personKey(name, sessionID)
	if h.holds.waiting(key) || h.blockedOnBox(ctx, name, agentType, sessionID) { // behind an earlier held message, or a box is open now
		if err := h.holdPerson(&personHeld{name: name, agentType: agentType, sessionID: sessionID, text: text, viaHub: viaHub, done: done}); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, h.deliverHumanSend(ctx, name, agentType, sessionID, text)
}

// held is how many of the agent's messages wait: those in its queue and the one taken off it that is waiting its turn or being typed (p.mu held). The loop running with nothing in
// hand counts none, and a message still in the queue is counted once, not again for the loop that is about to take it (the count was 2 for one message, and a bound of 2 refused the second).
func (p *personHolds) held(key string) int {
	n := len(p.queues[key])
	if p.releasing[key] {
		n++
	}
	return n
}

func (p *personHolds) waiting(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queues[key]) > 0 || p.running[key]
}

// holdPerson queues m behind the agent's earlier held messages and starts the agent's release loop when none runs.
func (h *SessionsHandler) holdPerson(m *personHeld) error {
	key := personKey(m.name, m.sessionID)
	p := &h.holds
	p.mu.Lock()
	if p.held(key) >= personHoldMaxPerAgent || p.total >= personHoldMaxTotal { // the one being released counts: it is still waiting for its turn to be typed
		p.mu.Unlock()
		return errSendBusy
	}
	if p.queues == nil {
		p.queues, p.running, p.releasing = map[string][]*personHeld{}, map[string]bool{}, map[string]bool{}
	}
	p.queues[key] = append(p.queues[key], m)
	p.total++
	start := !p.running[key]
	p.running[key] = true
	p.mu.Unlock()
	slog.Info("a person's message is held while the agent waits on a box", "session", m.name)
	if start {
		safego.Go("routes/person-hold", func() { h.releaseHeld(key) })
	}
	return nil
}

// releaseHeld types the agent's held messages one at a time, each once the agent is clear of boxes.
func (h *SessionsHandler) releaseHeld(key string) {
	p := &h.holds
	for {
		p.mu.Lock()
		q := p.queues[key]
		if len(q) == 0 {
			delete(p.queues, key)
			delete(p.running, key)
			p.mu.Unlock()
			return
		}
		m := q[0]
		p.queues[key] = q[1:]
		p.releasing[key] = true
		p.mu.Unlock()
		err := h.releaseOne(m)
		if err != nil {
			slog.Info("a held message of a person was not sent", "session", m.name, "error", err)
		}
		p.mu.Lock()
		delete(p.releasing, key)
		p.total--
		p.mu.Unlock()
		if m.done != nil {
			m.done(err)
		}
	}
}

// releaseOne waits until the agent has shown no box for personHoldSettle, then delivers m as a person's message.
func (h *SessionsHandler) releaseOne(m *personHeld) error {
	ctx, cancel := context.WithTimeout(context.Background(), personHoldMax)
	defer cancel()
	var clearSince time.Time
	for {
		st, ok := h.gateState(ctx, m.name, m.agentType, m.sessionID)
		switch {
		case ok && st.State == ptymanager.StateGone, !h.sessionStillLive(ctx, m.sessionID):
			return ptymanager.ErrTerminalGone // the session ended while the message waited
		case !ok, st.State == ptymanager.StateBlocked:
			// A box (the first, or the next one in a row): the wait starts again. A state that cannot be read now (the signals were there when the message was held) is no evidence
			// that the box is gone: it waits, until personHoldMax.
			clearSince = time.Time{}
		case clearSince.IsZero():
			clearSince = time.Now()
		case time.Since(clearSince) >= personHoldSettle:
			if m.viaHub && MaxModeFromSettings(h.ss)() < tunnel.TierInteract {
				return errSendModeLowered // the message waited with the authority a Coral Hub viewer at Interact had; it is not typed under a lower mode (Security's SHOULD on #6610)
			}
			sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer scancel()
			return h.deliverHumanSend(sctx, m.name, m.agentType, m.sessionID, m.text)
		}
		if err := sleepCtx(ctx, personHoldPoll); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return errSendHeldExpired
			}
			return err
		}
	}
}

// sessionStillLive reports whether the session is still a live one (a lookup that fails counts as still live: the wait goes on).
func (h *SessionsHandler) sessionStillLive(ctx context.Context, sessionID string) bool {
	if sessionID == "" || h.ss == nil {
		return true
	}
	ls, err := h.ss.GetLiveSession(ctx, sessionID)
	return err != nil || ls != nil
}

// heldNote is the answer to a message that was held: said plainly, so a person does not send it again.
const heldNote = "The agent is waiting on a question for you. Your message is held and is sent once the question is answered."
