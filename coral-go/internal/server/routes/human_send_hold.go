package routes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/ptymanager"

	"github.com/subgentic/coral/internal/cloud"
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

var (
	errSendCancelled = &errHumanSend{"cancelled", "Your queued message was cancelled and was not sent."}
	// errHeldSending: the message is being typed now; it can no longer be cancelled.
	errHeldSending  = &errHumanSend{"already_sending", "That message is being sent now, so it can't be cancelled."}
	errHeldNone     = &errHumanSend{"not_held", "There is no such queued message (it was already sent, refused or cancelled)."}
	errHeldNotYours = &errHumanSend{"not_yours", "That queued message was sent by someone else."}
)

// personHeld is a person's message waiting for an agent's boxes.
type personHeld struct {
	id                               string
	name, agentType, sessionID, text string
	heldAt                           time.Time
	viaHub                           bool // the message came through Coral Hub: it is typed only while this computer's mode still allows Interact, and only a sender from the same side may cancel it
	// done is called once, with how it ended (nil: typed and submitted); may be nil. Use finish.
	done func(error)
	// guarded by personHolds.mu: typing is set once the message is being typed (it can no longer be cancelled), cancelled when it was cancelled while it waited.
	typing, cancelled bool
	finished          sync.Once
}

// finish settles the message's waiter, once.
func (m *personHeld) finish(err error) {
	m.finished.Do(func() {
		if m.done != nil {
			m.done(err)
		}
	})
}

// personHolds is the messages waiting, per agent (key: its session id, else its terminal name): the head of a queue is the one waiting its turn to be typed, and stays in it until it is
// typed (or ended), so a cancel finds it. Its zero value is ready.
type personHolds struct {
	mu      sync.Mutex
	queues  map[string][]*personHeld
	running map[string]bool
	total   int
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

// deliverPersonSend is deliverHumanSend for a person's chat message, held while the agent waits on a box. heldID is not empty when the message was queued: it is typed later, and done (which
// may be nil) is called then with how that ended; the id names it for a cancel and in the live row's held_messages. When it is not held the message was delivered here, and done is not used.
func (h *SessionsHandler) deliverPersonSend(ctx context.Context, name, agentType, sessionID, text string, viaHub bool, done func(error)) (heldID string, err error) {
	key := personKey(name, sessionID)
	if h.holds.waiting(key) || h.blockedOnBox(ctx, name, agentType, sessionID) { // behind an earlier held message, or a box is open now
		m := &personHeld{id: newHeldID(), name: name, agentType: agentType, sessionID: sessionID, text: text, heldAt: time.Now().UTC(), viaHub: viaHub, done: done}
		if err := h.holdPerson(m); err != nil {
			return "", err
		}
		return m.id, nil
	}
	return "", h.deliverHumanSend(ctx, name, agentType, sessionID, text)
}

// newHeldID is a held message's id: unguessable, short.
func newHeldID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "h" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "h" + hex.EncodeToString(b)
}

// held is how many of the agent's messages wait (p.mu held). The one waiting its turn stays in the queue until it is typed, so it is counted once, and the loop running with nothing in hand
// counts none (counting the loop as well gave 2 for one message and a bound of 2 refused the second, the CI run of #6610).
func (p *personHolds) held(key string) int { return len(p.queues[key]) }

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
	if p.held(key) >= personHoldMaxPerAgent || p.total >= personHoldMaxTotal {
		p.mu.Unlock()
		return errSendBusy
	}
	if p.queues == nil {
		p.queues, p.running = map[string][]*personHeld{}, map[string]bool{}
	}
	p.queues[key] = append(p.queues[key], m)
	p.total++
	start := !p.running[key]
	p.running[key] = true
	p.mu.Unlock()
	slog.Info("a person's message is held while the agent waits on a box", "session", m.name)
	h.kickWS() // the live row now lists it
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
		p.mu.Unlock()
		err := h.releaseOne(m)
		if err != nil && !errors.Is(err, errSendCancelled) {
			slog.Info("a held message of a person was not sent", "session", m.name, "error", err)
		}
		p.mu.Lock()
		p.removeLocked(key, m) // a cancel has taken it out already
		p.mu.Unlock()
		h.kickWS() // the live row no longer lists it
		m.finish(err)
	}
}

// removeLocked takes m out of the agent's queue when it is still in it (p.mu held).
func (p *personHolds) removeLocked(key string, m *personHeld) {
	q := p.queues[key]
	for i, x := range q {
		if x == m {
			p.queues[key] = append(q[:i:i], q[i+1:]...)
			p.total--
			return
		}
	}
}

// cancelHeld drops one held message before it is typed and settles its waiter as cancelled. viaHub is where the request to cancel came from: a sender at the dashboard of this computer may cancel
// any (it is the owner's computer); one through Coral Hub only those sent through it (Coral Hub does not tell this computer which Hub user sent a message, so the Hub's viewers at Interact, who can
// all send to the agent, share them). A message that is being typed can't be cancelled.
func (h *SessionsHandler) cancelHeld(name, id string, viaHub bool) error {
	p := &h.holds
	p.mu.Lock()
	var m *personHeld
	var key string
	for k, q := range p.queues { // at most personHoldMaxTotal messages: the id names it, so no session or query is needed (a Coral Hub request carries no other parameters)
		for _, x := range q {
			if x.id == id && x.name == name {
				m, key = x, k
			}
		}
	}
	switch {
	case m == nil:
		p.mu.Unlock()
		return errHeldNone
	case viaHub && !m.viaHub:
		p.mu.Unlock()
		return errHeldNotYours
	case m.typing:
		p.mu.Unlock()
		return errHeldSending
	}
	m.cancelled = true
	p.removeLocked(key, m)
	p.mu.Unlock()
	h.kickWS()
	m.finish(errSendCancelled)
	return nil
}

// heldMessage is one held message as the live row lists it.
type heldMessage struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	HeldAt  string `json:"held_at"`
	Sending bool   `json:"sending"` // being typed now: it can no longer be cancelled
}

// heldMessagesOf is heldMessages for a live row: nil (a null) when there are none.
func (h *SessionsHandler) heldMessagesOf(sessionID, name string) any {
	if held := h.heldMessages(sessionID, name); len(held) > 0 {
		return held
	}
	return nil
}

// heldMessages is the agent's held messages, oldest first, or nil when there are none (a viewer who can only look never receives them: the Coral Hub connector nulls the field, pkg/tunnel/viewdrop.go).
func (h *SessionsHandler) heldMessages(sessionID, name string) []heldMessage {
	p := &h.holds
	p.mu.Lock()
	defer p.mu.Unlock()
	q := p.queues[personKey(name, sessionID)]
	if len(q) == 0 {
		return nil
	}
	out := make([]heldMessage, 0, len(q))
	for _, m := range q {
		out = append(out, heldMessage{ID: m.id, Text: m.text, HeldAt: m.heldAt.Format(time.RFC3339), Sending: m.typing})
	}
	return out
}

// releaseOne waits until the agent has shown no box for personHoldSettle, then delivers m as a person's message. A cancel ends the wait.
func (h *SessionsHandler) releaseOne(m *personHeld) error {
	ctx, cancel := context.WithTimeout(context.Background(), personHoldMax)
	defer cancel()
	p := &h.holds
	var clearSince time.Time
	for {
		p.mu.Lock()
		cancelled := m.cancelled
		p.mu.Unlock()
		if cancelled {
			return errSendCancelled
		}
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
			p.mu.Lock()
			if m.cancelled {
				p.mu.Unlock()
				return errSendCancelled
			}
			m.typing = true // from here a cancel is refused
			p.mu.Unlock()
			h.kickWS()
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

// CancelHeld drops one of a person's held chat messages before it is typed (#6627).
// DELETE /api/sessions/live/{name}/held/{id}
// An Operator-only route (identity_routes.go: an agent's token is refused before it gets here), and Coral Hub at Interact; the id names the message (the agent is the path's). 409 already_sending for a message being
// typed now, 404 not_held for one that is gone, 403 not_yours for a Coral Hub sender cancelling one sent from the dashboard.
func (h *SessionsHandler) CancelHeld(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	id := chi.URLParam(r, "id")
	err := h.cancelHeld(name, id, cloud.Tunneled(r))
	var e *errHumanSend
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": id})
	case errors.As(err, &e):
		status := http.StatusConflict
		switch e {
		case errHeldNone:
			status = http.StatusNotFound
		case errHeldNotYours:
			status = http.StatusForbidden
		}
		writeJSON(w, status, map[string]any{"error": e.text, "code": e.code})
	default:
		errInternalServer(w, err.Error())
	}
}
