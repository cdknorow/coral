package ptymanager

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/inputlog"
	"github.com/cdknorow/coral/internal/tmux"
)

// The automated-input gate (#3431).
//
// Coral types automated text into agents: board notifications and nudges, task
// notifications, message-wait results, the remote poller, a launch's first
// prompt, agent UI notices. Typed while the agent sits at a prompt for the user
// (a permission or selection menu, an approval question, the trust-this-folder
// screen, a model or login picker), the text plus Enter answers that prompt, and
// on some options auto-accepts it with no human decision. So every automated
// send goes through DeliverAutomated. It types only when the agent is NOT
// waiting on a prompt: neither its state (hook events, NeedsInputFunc) nor its
// screen, read just before typing, shows one. Otherwise the send is held and
// delivered once the prompt is gone, exactly once; a later send with the same
// Key replaces a held one (a newer unread count supersedes an older one).
//
// Explicit human input never goes through here: the operator typing in the
// terminal or the dashboard, and a person picking a prompt option
// (prompt_answer.go). The opt-in auto-accept feature answers approval prompts on
// purpose and uses the AutoAcceptSend exemption, which nothing else may use.

// Delivery says what DeliverAutomated did with a send.
type Delivery int

const (
	// Sent: the text was typed now.
	Sent Delivery = iota
	// Held: the agent is waiting on a prompt; the text is typed once it isn't.
	Held
)

func (d Delivery) String() string {
	if d == Held {
		return "held"
	}
	return "sent"
}

// AutomatedInput is one automated send.
type AutomatedInput struct {
	// Term is the terminal or runtime the text goes to, read for the screen
	// (ScreenReader).
	Term any
	// Name, AgentType and SessionID identify the agent's session.
	Name, AgentType, SessionID string
	// Key identifies what the send is about. A send with the same session and
	// Key replaces a held one; "" means the text itself.
	Key string
	// Text is what will be typed, for the input debug log.
	Text string
	// What names the sender in logs ("board message", "initial prompt", ...).
	What string
	// Send types the text. It runs only when nothing is waiting on the user.
	Send func(ctx context.Context) error
	// RequireInputBox holds the send until the agent's normal input box is on
	// screen, not just until no prompt is: a launch's first prompt, which a
	// still-drawing trust or login screen could otherwise take as its answer.
	// After the held-too-long time it falls back to the usual rule (no prompt
	// open), so an input box Coral doesn't recognise can't hold a launch for
	// ever; an open prompt still holds it.
	RequireInputBox bool
}

// EnterSender is implemented by terminals that can press Enter in an agent's
// terminal: a send whose Enter was withheld because a prompt opened between its
// text and its Enter (tmux.ErrEnterWithheld) holds just that Enter.
type EnterSender interface {
	SendEnter(ctx context.Context, name, agentType, sessionID string) error
}

// ScreenReader is implemented by terminals that can show an agent's visible
// screen (the bottom of its tmux pane, or its PTY output).
type ScreenReader interface {
	AgentScreen(ctx context.Context, name, agentType, sessionID string) (string, error)
}

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
// being typed into the prompt).
var heldTooLong struct {
	sync.RWMutex
	fn func(name, agentType, sessionID, what string)
}

// SetHeldTooLongFunc installs the callback for sends held a while.
func SetHeldTooLongFunc(fn func(name, agentType, sessionID, what string)) {
	heldTooLong.Lock()
	heldTooLong.fn = fn
	heldTooLong.Unlock()
}

// gateTimings are the gate's intervals (read under the gate's lock; tests shorten them).
type gateTimings struct {
	// tooLong is how long a send waits before HeldTooLongFunc is told.
	tooLong time.Duration
	// expiry drops a nudge or notification still held after this long (the
	// prompt was never answered): a day-old nudge typed later is noise. A
	// first prompt (RequireInputBox) has no time limit; it goes with its agent
	// (DropHeld).
	expiry time.Duration
	// every is how often held sends are retried.
	every time.Duration
}

// heldPerSession bounds the held sends kept for one agent.
const heldPerSession = 50

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

// waiting reports whether this send must wait: the agent is waiting on the
// user, or (RequireInputBox) its input box isn't on screen yet.
func (in AutomatedInput) waiting(ctx context.Context) (bool, string) {
	if waiting, why := WaitingOnUser(ctx, in.Term, in.Name, in.AgentType, in.SessionID); waiting {
		return true, why
	}
	if !in.RequireInputBox {
		return false, ""
	}
	r, ok := in.Term.(ScreenReader)
	if !ok {
		return false, "" // can't tell: the caller's own startup wait applies
	}
	screen, err := r.AgentScreen(ctx, in.Name, in.AgentType, in.SessionID)
	if err != nil || !InputBoxOnScreen(screen) {
		return true, "the agent's input box isn't on screen yet"
	}
	return false, ""
}

// DeliverAutomated types an automated send now if the agent is not waiting on
// the user, and holds it otherwise. A held send is retried until the prompt is
// gone and then typed once.
func DeliverAutomated(ctx context.Context, in AutomatedInput) (Delivery, error) {
	if in.Send == nil {
		return Sent, errors.New("automated input without a send function")
	}
	if in.Key == "" {
		in.Key = in.Text
	}
	unlock := gate.lockSession(in.Name)
	defer unlock()
	if waiting, why := in.waiting(ctx); waiting {
		gate.hold(in, why)
		return Held, nil
	}
	// Anything already held for this agent goes first, so order is kept.
	if gate.flush(ctx, in.Name) {
		// The flush found the agent waiting again: queue behind what it held.
		gate.hold(in, "earlier sends still held")
		return Held, nil
	}
	enterHeld, err := gate.sendChecked(ctx, in)
	if enterHeld {
		return Held, nil
	}
	return Sent, err
}

// sendChecked runs in.Send with the screen re-checked just before its final
// Enter. If a prompt opened after the text was typed, the Enter is withheld and
// held on its own, ahead of anything else held for the agent; it reports true
// then. Any other error is returned.
func (g *inputGate) sendChecked(ctx context.Context, in AutomatedInput) (enterHeld bool, err error) {
	check := func(ctx context.Context) bool {
		r, ok := in.Term.(ScreenReader)
		if !ok {
			return true
		}
		screen, err := r.AgentScreen(ctx, in.Name, in.AgentType, in.SessionID)
		if err != nil {
			return true
		}
		open, _ := PromptOnScreen(screen)
		return !open
	}
	err = in.Send(tmux.WithBeforeEnter(ctx, check))
	if errors.Is(err, tmux.ErrEnterWithheld) {
		g.holdEnter(in)
		return true, nil
	}
	return false, err
}

// holdEnter holds the Enter of a send whose text was typed but whose Enter was
// withheld, first in the agent's queue: it belongs to the text already typed.
func (g *inputGate) holdEnter(in AutomatedInput) {
	enter := AutomatedInput{Term: in.Term, Name: in.Name, AgentType: in.AgentType, SessionID: in.SessionID,
		Key: in.Key + "#enter", Text: "[Enter] " + in.Text, What: in.What + " (its Enter)",
		Send: func(ctx context.Context) error {
			es, ok := in.Term.(EnterSender)
			if !ok {
				return errors.New("this terminal can't press Enter on its own")
			}
			return es.SendEnter(ctx, in.Name, in.AgentType, in.SessionID)
		}}
	g.mu.Lock()
	g.held[in.Name] = append([]*heldInput{{in: enter, heldAt: g.now(), why: "a prompt opened before its Enter"}}, g.held[in.Name]...)
	start := !g.running
	g.running = true
	g.mu.Unlock()
	g.logHeld(enter, "a prompt opened between its text and its Enter", "Enter withheld")
	if start {
		go g.loop()
	}
}

// AutoAcceptSend is the one automated path allowed to type into an open
// prompt: the opt-in auto-accept feature answering an approval it judged safe.
// It is logged in the input debug log. Nothing else may call it (a test
// enforces that).
func AutoAcceptSend(ctx context.Context, name string, send func(ctx context.Context) error) error {
	inputlog.Record(name, inputlog.Keys, "[auto-accept answers an approval prompt]")
	return send(ctx)
}

type heldInput struct {
	in       AutomatedInput
	heldAt   time.Time
	why      string
	reported bool
}

type inputGate struct {
	mu      sync.Mutex
	held    map[string][]*heldInput // by session name, oldest first
	running bool
	now     func() time.Time
	// sending serialises the sends to one session (a direct send and the
	// retry loop), so held sends keep their order and none is typed twice.
	sending sync.Map // session name -> *sync.Mutex
	timings gateTimings
}

var gate = &inputGate{held: map[string][]*heldInput{}, now: time.Now,
	timings: gateTimings{tooLong: 2 * time.Minute, expiry: time.Hour, every: 2 * time.Second}}

// setTimings replaces the gate's intervals and returns the previous ones (tests).
func (g *inputGate) setTimings(t gateTimings) gateTimings {
	g.mu.Lock()
	defer g.mu.Unlock()
	prev := g.timings
	g.timings = t
	return prev
}

func (g *inputGate) lockSession(name string) func() {
	m, _ := g.sending.LoadOrStore(name, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (g *inputGate) hold(in AutomatedInput, why string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	q := g.held[in.Name]
	for i, h := range q {
		if h.in.Key == in.Key {
			// Superseded: the newer send replaces it, in its place.
			q[i] = &heldInput{in: in, heldAt: h.heldAt, why: why, reported: h.reported}
			g.held[in.Name] = q
			g.logHeld(in, why, "replaced a held send")
			return
		}
	}
	if len(q) >= heldPerSession {
		slog.Warn("automated input: too many held sends; dropping the oldest", "session", in.Name, "dropped", q[0].in.What)
		q = q[1:]
	}
	g.held[in.Name] = append(q, &heldInput{in: in, heldAt: g.now(), why: why})
	g.logHeld(in, why, "held")
	if !g.running {
		g.running = true
		go g.loop()
	}
}

func (g *inputGate) logHeld(in AutomatedInput, why, what string) {
	slog.Info("automated input "+what+": the agent is waiting on a prompt", "session", in.Name, "sender", in.What, "why", why)
	inputlog.Record(in.Name, inputlog.Text, "[HELD, "+why+"] "+in.Text)
}

// flush types what is held for name if the agent is no longer waiting. It
// reports whether something is still held (the agent is waiting again).
func (g *inputGate) flush(ctx context.Context, name string) bool {
	for {
		g.mu.Lock()
		q := g.held[name]
		if len(q) == 0 {
			delete(g.held, name)
			g.mu.Unlock()
			return false
		}
		h := q[0]
		g.mu.Unlock()

		// Checked again just before each send: the prompt may come back.
		in := h.in
		g.mu.Lock()
		if in.RequireInputBox && g.now().Sub(h.heldAt) > g.timings.tooLong {
			in.RequireInputBox = false
		}
		g.mu.Unlock()
		if waiting, _ := in.waiting(ctx); waiting {
			return true
		}
		g.mu.Lock()
		// Remove it before typing, so it is typed once even if Send fails.
		if cur := g.held[name]; len(cur) > 0 && cur[0] == h {
			g.held[name] = cur[1:]
		} else {
			g.mu.Unlock()
			continue // replaced meanwhile: look again
		}
		g.mu.Unlock()
		if enterHeld, err := g.sendChecked(ctx, h.in); enterHeld {
			return true
		} else if err != nil {
			slog.Warn("automated input: a held send failed", "session", name, "sender", h.in.What, "error", err)
		} else {
			slog.Info("automated input delivered after the prompt closed", "session", name, "sender", h.in.What, "held_for", g.now().Sub(h.heldAt).Round(time.Second).String())
		}
	}
}

func (g *inputGate) loop() {
	for {
		g.mu.Lock()
		every := g.timings.every
		g.mu.Unlock()
		time.Sleep(every)
		g.mu.Lock()
		names := make([]string, 0, len(g.held))
		for n := range g.held {
			names = append(names, n)
		}
		if len(names) == 0 {
			g.running = false
			g.mu.Unlock()
			return
		}
		g.expireAndReportLocked()
		g.mu.Unlock()
		for _, n := range names {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			unlock := g.lockSession(n)
			g.flush(ctx, n)
			unlock()
			cancel()
		}
	}
}

// expireAndReportLocked drops sends held past the expiry and reports, once per
// send, the ones held longer than tooLong.
func (g *inputGate) expireAndReportLocked() {
	now := g.now()
	heldTooLong.RLock()
	report := heldTooLong.fn
	heldTooLong.RUnlock()
	for name, q := range g.held {
		kept := q[:0]
		for _, h := range q {
			if !h.in.RequireInputBox && now.Sub(h.heldAt) > g.timings.expiry {
				slog.Warn("automated input: dropping a send held too long", "session", name, "sender", h.in.What)
				continue
			}
			if !h.reported && now.Sub(h.heldAt) > g.timings.tooLong && report != nil {
				h.reported = true
				go report(h.in.Name, h.in.AgentType, h.in.SessionID, h.in.What)
			}
			kept = append(kept, h)
		}
		if len(kept) == 0 {
			delete(g.held, name)
		} else {
			g.held[name] = kept
		}
	}
}

// DropHeld discards everything held for a session: its agent is being killed,
// restarted, resumed or launched again in that terminal, and a send held for
// the old process (a stale nudge, or a first prompt) must never be typed into
// the new one (#3431). The terminals call it from their create, kill, restart
// and launch-command paths.
func DropHeld(name, why string) {
	name = sessionOfTarget(name)
	gate.mu.Lock()
	n := len(gate.held[name])
	delete(gate.held, name)
	gate.mu.Unlock()
	if n > 0 {
		slog.Info("automated input: dropped held sends", "session", name, "count", n, "why", why)
		inputlog.Record(name, inputlog.Text, "[DROPPED "+strconv.Itoa(n)+" held, "+why+"]")
	}
}

// sessionOfTarget is the session a tmux target names: "claude-x", "claude-x.0",
// "claude-x:0" and "claude-x:0.0" are all claude-x (QA's #3431 v3 live check:
// the restart and launch paths pass pane targets).
func sessionOfTarget(target string) string {
	if i := strings.IndexByte(target, ':'); i >= 0 {
		target = target[:i]
	}
	return strings.TrimSuffix(target, ".0")
}

// HeldCount is the number of sends held for a session (tests and diagnostics).
func HeldCount(name string) int {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return len(gate.held[name])
}
