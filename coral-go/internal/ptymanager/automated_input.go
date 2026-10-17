package ptymanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cdknorow/coral/internal/inputlog"
	"github.com/cdknorow/coral/internal/logdedupe"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/safego"
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
	// Mention: the send is about a board message that @-mentions this agent (by the board's
	// own rule, numbered names included). In signals mode it is typed in mid-turn, as with the
	// screen rule; a routine nudge or message waits for the turn to end (#3578, the Operator's
	// decision on #3494). A prompt or a menu on screen still holds it.
	Mention bool
	// RequireInputBox holds the send until the agent's normal input box is on
	// screen, not just until no prompt is: a launch's first prompt, which a
	// still-drawing trust or login screen could otherwise take as its answer.
	// After the held-too-long time it falls back to the usual rule (no prompt
	// open), so an input box Coral doesn't recognise can't hold a launch for
	// ever; an open prompt still holds it.
	RequireInputBox bool
	// Paste sends Text as one bracketed paste and then Enter where the agent's
	// terminal can (PromptSender, with bracketed paste on), so a menu that opens just
	// before it ignores it instead of taking it as an answer (#3457); Send is then
	// the fallback, for a terminal or agent without bracketed paste. Ready, when
	// set, runs just before the paste (the caller's has-the-agent-exited check).
	Paste bool
	Ready func(ctx context.Context) error

	// pasteRetry: a held paste send. Its text may already be in the input box
	// (only its Enter is left), or a menu dropped it (pasted again, once).
	pasteRetry int
	// hadPlaceholder: the box held a "[Pasted ...]" placeholder before Coral's paste
	// (a person's), so one is never taken as ours.
	hadPlaceholder bool
	// entered: its text was pasted and its Enter pressed, but the text was still in the box
	// afterwards (#4104: a dialog took the Enter). Its next turn presses only the Enter, never
	// pastes again, and an empty box then means the Enter took effect late: delivered.
	// enterTries counts those Enters with no dialog up; after maxEnterTries it is given up.
	entered    bool
	enterTries int
	// urgent: an @-mention waits behind this held send (#3708), so a paste of Coral's that
	// can't be confirmed gives up after pasteStuckAfterUrgent instead of pasteStuckAfter.
	urgent bool
	// heldSince, heldWhy and heldReported carry a held send's first hold time, its
	// reason and whether it was reported across its turns (a paste send is taken out
	// of the queue and held again on each): it ages to the held-too-long notice and
	// the expiry, and is logged once per reason (#3468).
	heldSince    time.Time
	heldWhy      string
	heldReported bool
	// heldReportedPrompt: it was reported as held by a prompt (prompt=true). A send
	// first reported as an input-box hold is reported again once a prompt holds it.
	heldReportedPrompt bool
	// gen is the session's DropHeld generation when the send reached the gate: a send
	// in flight while DropHeld ran (a turn that takes it off the queue and holds it
	// again) is never held again for the new process (#3468 v2).
	gen uint64
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

// gateTimings are the gate's intervals (read under the gate's lock; tests shorten them).
type gateTimings struct {
	// tooLong is how long a send waits before HeldTooLongFunc is told.
	tooLong time.Duration
	// expiry drops a nudge or notification still held after this long (the
	// prompt was never answered): a day-old nudge typed later is noise. A
	// first prompt (RequireInputBox) has no time limit; it goes with its agent
	// (DropHeld).
	expiry time.Duration
	// dialogReport: a send held behind a dialog (#4104) is reported after this, not tooLong:
	// the user has something to dismiss now. dialogMentionGiveUp: a mention held behind one is
	// given up after this (never pasted, never typed into the dialog). Zero: the defaults.
	dialogReport, dialogMentionGiveUp time.Duration
	// every is how often held sends are retried.
	every time.Duration
}

// heldPerSession bounds the held sends kept for one agent.
const heldPerSession = 50

// heldBytesPerSession caps the text of a session's held sends (#3578).
const heldBytesPerSession = 256 << 10

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
	gate.shadowLog(in, waiting, why, sWait, sWhy, sKind, st, decided)
	if decided == "signals" {
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
	gate.mu.Lock()
	in.gen = gate.gens[in.Name]
	gate.mu.Unlock()
	if waiting, why, kind := in.waiting(ctx); waiting {
		gate.hold(in, why, kind)
		return Held, nil
	}
	// Anything already held for this agent goes first, so order is kept. A mention lets a
	// stuck paste of Coral's ahead of it give up sooner (#3708).
	fctx := ctx
	if in.Mention {
		fctx = context.WithValue(ctx, mentionWaitingKey{}, true)
	}
	if gate.flush(fctx, in.Name) {
		// The flush found the agent waiting again: queue behind what it held.
		gate.hold(in, "earlier sends still held", "")
		return Held, nil
	}
	enterHeld, err := gate.sendChecked(ctx, in)
	if enterHeld {
		return Held, nil
	}
	if errors.Is(err, ErrTeamBusy) {
		// The team's next slot is over MaxTeamWait away (#3613): held, and the loop tries again.
		gate.hold(in, whyTeamSpaced, "")
		return Held, nil
	}
	gate.shadowForget(in.Name, in.Key)
	if err == nil {
		gate.noteDelivered(in.Name, in.Key)
	}
	return Sent, err
}

// sendChecked runs in.Send with the screen re-checked just before its final
// Enter. If a prompt opened after the text was typed, the Enter is withheld and
// held on its own, ahead of anything else held for the agent; it reports true
// then. Any other error is returned.
func (g *inputGate) sendChecked(ctx context.Context, in AutomatedInput) (enterHeld bool, err error) {
	// Coral's automated sends to one team are spaced (#3613): the first goes at once, the next
	// 2-3 s after. Only here, where a send really goes: a held send waits nothing until it's due.
	if err := SpaceTeam(ctx, teamFor(in.Name)); err != nil {
		return false, err
	}
	if in.Paste {
		if held, err, done := g.pasteChecked(ctx, in); done {
			return held, err
		}
	}
	var footerOnly atomic.Bool // the prompt seen was only a footer (isPromptHold)
	check := func(ctx context.Context) bool {
		r, ok := in.Term.(ScreenReader)
		if !ok {
			return true
		}
		screen, err := r.AgentScreen(ctx, in.Name, in.AgentType, in.SessionID)
		if err != nil {
			return true
		}
		open, why := PromptOnScreen(screen)
		footerOnly.Store(open && !isPromptHold(why))
		return !open
	}
	err = in.Send(tmux.WithBeforeEnter(ctx, check))
	if errors.Is(err, tmux.ErrEnterWithheld) {
		kind := kindPrompt
		if footerOnly.Load() {
			kind = ""
		}
		g.holdEnter(in, kind)
		return true, nil
	}
	return false, err
}

// pasteChecked sends in as a bracketed paste and its Enter. Enter goes only when no
// prompt is open and (on a rendered screen) the input box shows the text; otherwise
// the send is held first in the agent's queue, and on its turn sends just its Enter
// if the text is in the box, or pastes again (once) if a menu dropped it. done is
// false when the paste can't be used (no PromptSender, bracketed paste off or
// unknown): the caller falls back to typing.
func (g *inputGate) pasteChecked(ctx context.Context, in AutomatedInput) (enterHeld bool, err error, done bool) {
	ps, ok := in.Term.(PromptSender)
	if !ok {
		return false, nil, false
	}
	if in.Ready != nil {
		if err := in.Ready(ctx); err != nil {
			return false, err, true
		}
	}
	agentType, sessionID := in.identity()
	sr, _ := in.Term.(ScreenReader)
	rendered := false
	if rs, ok := in.Term.(RenderedScreen); ok && sr != nil {
		rendered = rs.RenderedScreen()
	}
	if in.pasteRetry > 0 && sr != nil {
		// Its turn again (#3457 v2): what is in the input box decides. Our text: only
		// its Enter. Empty (or a placeholder): a menu dropped the paste, so paste
		// again, at most maxPasteRetries times. Anything else (a person's draft, or
		// text Coral can't recognise): never added to; held, and reported.
		screen, serr := sr.AgentScreen(ctx, in.Name, agentType, sessionID)
		if serr != nil {
			if g.pasteStuck(in) {
				return false, g.giveUpPaste(in, "the screen couldn't be read"), true
			}
			g.holdPaste(in, "the screen couldn't be read", false, "")
			return true, nil, true
		}
		if open, why := PromptOnScreen(screen); open {
			g.holdPaste(in, "a prompt is still open", false, promptKind(why))
			return true, nil, true
		}
		state := boxOurs // a PTY's raw output can't show the box: Enter only, never paste again
		if rendered {
			state = in.boxState(ctx, screen, agentType, sessionID)
		} else {
			slog.Info("automated input: unconfirmed delivery (the terminal's box can't be read; sending only the Enter)",
				"session", in.Name, "sender", in.What)
		}
		switch state {
		case boxOurs:
			held, err := g.enterAndConfirm(ctx, in, sr, rendered, agentType, sessionID)
			return held, err, true
		case boxEmpty:
			if in.entered {
				// Its Enter took effect after the last look: delivered, never pasted again.
				g.setLastPasted(in.Name, "")
				return false, nil, true
			}
			if in.pasteRetry > maxPasteRetries {
				if g.pasteStuck(in) {
					return false, g.giveUpPaste(in, "pasted again but it never reached the input box"), true
				}
				g.holdPaste(in, "pasted again but it never reached the input box", false, "")
				return true, nil, true
			}
		case boxOther:
			// Text Coral can't place is in the box after Coral's own paste: never Entered (it
			// could be a person's draft), and given up once stuck rather than blocking (#3708).
			if g.pasteStuck(in) {
				return false, g.giveUpPaste(in, "the input box holds text Coral can't place"), true
			}
			g.holdPaste(in, whyDraft, false, "")
			return true, nil, true
		default:
			if g.pasteStuck(in) {
				return false, g.giveUpPaste(in, "the input box couldn't be found on screen"), true
			}
			g.holdPaste(in, "the input box isn't on screen", false, "")
			return true, nil, true
		}
	}
	if in.pasteRetry == 0 && rendered {
		// Before the first paste (#3457 v3): a box holding someone else's text (a
		// person's draft, or their own paste) is never added to, so it is never
		// submitted with ours; the send waits. Our text already there (an earlier
		// attempt): only its Enter. Checked for every agent type (#3478: agy's
		// drafts were skipped because its box wasn't listed as known): an input
		// line holding other text holds the send whatever the agent.
		if screen, serr := sr.AgentScreen(ctx, in.Name, agentType, sessionID); serr == nil {
			in.hadPlaceholder = hasPlaceholder(screen)
			switch in.boxState(ctx, screen, agentType, sessionID) {
			case boxOther:
				if held, err, own := g.submitLeftover(ctx, in, sr, screen, agentType, sessionID); own {
					if held || err != nil {
						return held, err, true
					}
					break // Coral's earlier text was sent: now ours
				}
				g.holdPaste(in, whyDraft, false, "")
				return true, nil, true
			case boxOurs:
				held, err := g.enterAndConfirm(ctx, in, sr, rendered, agentType, sessionID)
				return held, err, true
			}
		}
	}
	pctx := ctx
	if _, known := BracketedPasteHint(ctx); !known {
		if enabled, known := BracketedPasteFromLog(naming.DefaultLogDir(), agentType, sessionID); known {
			pctx = WithBracketedPasteHint(ctx, enabled)
		}
	}
	var promptOpened atomic.Bool
	check := func(ctx context.Context) bool {
		if sr == nil {
			return true
		}
		// The agent repaints its box a moment after the paste: look for the text for
		// up to pasteVerifyWait before deciding it didn't land (#3457 v2).
		deadline := time.Now().Add(pasteVerifyWait)
		for {
			screen, err := sr.AgentScreen(ctx, in.Name, agentType, sessionID)
			if err != nil {
				return true
			}
			if open, why := PromptOnScreen(screen); open {
				promptOpened.Store(isPromptHold(why))
				return false
			}
			if !rendered || inputBoxState(screen, in.Text, in.hadPlaceholder) == boxOurs {
				return true
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				return false
			}
			time.Sleep(pasteVerifyEvery)
		}
	}
	g.setLastPasted(in.Name, in.Text) // Coral's, until its Enter is seen to submit it (#4104)
	err = ps.SendPrompt(tmux.WithBeforeEnter(pctx, check), in.Name, in.Text, agentType, sessionID)
	if err == nil {
		held, err := g.confirmSubmitted(ctx, in, sr, rendered, agentType, sessionID)
		return held, err, true
	}
	switch {
	case errors.Is(err, ErrBracketedPasteUnavailable):
		return false, nil, false
	case errors.Is(err, tmux.ErrEnterWithheld):
		if promptOpened.Load() {
			g.holdPaste(in, "a prompt opened before its Enter", true, kindPrompt)
		} else {
			g.holdPaste(in, "the paste didn't reach the input box", true, "")
		}
		return true, nil, true
	}
	return false, err, true
}

// whyDraft: a paste send held because the input box holds text that isn't Coral's.
const whyDraft = "a draft is in the input box (someone else's text); not adding to it"

// releasedBecause says what ended a hold, from its last reason, for the delivery's log
// line: a draft hold ends when the draft is cleared, not when a prompt closes (#3520).
// whyTeamSpaced: a send held because its team's next slot was over MaxTeamWait away (#3613).
const whyTeamSpaced = "the team's sends are spaced"

func releasedBecause(why string) string {
	switch {
	case why == whyTeamSpaced:
		return "the team's slot came"
	case why == whyDraft:
		return "the draft cleared"
	case strings.Contains(why, "input box"):
		return "the input box was ready"
	case why == "earlier sends still held":
		return "the earlier sends were typed"
	case strings.HasPrefix(why, "signals: starting"):
		return "the agent started"
	case why == "signals: in turn":
		return "the turn ended"
	case strings.HasPrefix(why, busyWhy):
		return "the agent stopped working"
	}
	return "the prompt closed"
}

// boxState is inputBoxState for this send, where a box that seems to hold someone
// else's text is checked once more on the styled screen: Claude's faint prompt
// suggestion is an empty box (#3468). A "[Pasted" placeholder is never one.
func (in AutomatedInput) boxState(ctx context.Context, screen, agentType, sessionID string) boxState {
	st := inputBoxState(screen, in.Text, in.hadPlaceholder)
	if st != boxOther || hasPlaceholder(screen) {
		return st
	}
	if ss, ok := in.Term.(StyledScreenReader); ok {
		if styled, err := ss.AgentScreenStyled(ctx, in.Name, agentType, sessionID); err == nil && suggestionOnly(styled) {
			return boxEmpty
		}
	}
	return st
}

// maxPasteRetries bounds pasting a send again into an empty box: after that it stays
// held (and is reported) rather than pasted once more.
const maxPasteRetries = 1

// pasteVerifyWait and pasteVerifyEvery: how long, and how often, the gate looks for
// pasted text in the input box before deciding the paste didn't land (variables for
// tests).
var (
	pasteVerifyWait  = time.Second
	pasteVerifyEvery = 100 * time.Millisecond
)

// holdPaste holds a paste send whose Enter was withheld, first in the agent's queue.
// pasted says the text was just pasted (a later turn may paste it once more); prompt
// that an open prompt holds it (HeldTooLongFunc).
func (g *inputGate) holdPaste(in AutomatedInput, why string, pasted bool, kind string) {
	if pasted {
		in.pasteRetry++
	}
	g.mu.Lock()
	if g.staleLocked(in) {
		g.mu.Unlock()
		return
	}
	heldAt := in.heldSince
	if heldAt.IsZero() {
		heldAt = g.now()
	}
	in.heldSince = heldAt
	again := in.heldWhy == why
	in.heldWhy = why
	g.held[in.Name] = append([]*heldInput{{in: in, heldAt: heldAt, why: why, kind: kind, reported: in.heldReported, reportedPrompt: in.heldReportedPrompt}}, g.held[in.Name]...)
	g.noteDialogLocked(in.Name, kind)
	start := !g.running
	g.running = true
	g.mu.Unlock()
	if !again {
		g.logHeld(in, why, "paste held")
	}
	if start {
		go func() { defer safego.Recover("ptymanager/automated_input:g.loop"); g.loop() }()
	}
}

// holdEnter holds the Enter of a send whose text was typed but whose Enter was
// withheld, first in the agent's queue: it belongs to the text already typed.
func (g *inputGate) holdEnter(in AutomatedInput, kind string) {
	enter := AutomatedInput{Term: in.Term, Name: in.Name, AgentType: in.AgentType, SessionID: in.SessionID,
		Key: in.Key + "#enter", Text: "[Enter] " + in.Text, What: in.What + " (its Enter)",
		Send: func(ctx context.Context) error {
			es, ok := in.Term.(EnterSender)
			if !ok {
				return errors.New("this terminal can't press Enter on its own")
			}
			return es.SendEnter(ctx, in.Name, in.AgentType, in.SessionID)
		}}
	enter.gen = in.gen
	g.mu.Lock()
	if g.staleLocked(in) {
		g.mu.Unlock()
		return
	}
	heldAt := in.heldSince // a held send's Enter keeps its age and report
	if heldAt.IsZero() {
		heldAt = g.now()
	}
	enter.heldSince, enter.heldReported, enter.heldReportedPrompt = heldAt, in.heldReported, in.heldReportedPrompt
	g.held[in.Name] = append([]*heldInput{{in: enter, heldAt: heldAt, why: "a prompt opened before its Enter", kind: kind, reported: in.heldReported, reportedPrompt: in.heldReportedPrompt}}, g.held[in.Name]...)
	start := !g.running
	g.running = true
	g.mu.Unlock()
	g.logHeld(enter, "a prompt opened between its text and its Enter", "Enter withheld")
	if start {
		go func() { defer safego.Recover("ptymanager/automated_input:g.loop"); g.loop() }()
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
	kind     string // what holds it when it is a request to the user ("" otherwise; HeldTooLongFunc)
	reported bool
	// reportedPrompt: reported with prompt=true (a calm report is followed by a
	// prompt one if a prompt holds it later).
	reportedPrompt bool
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
	// reportedFor: sessions with a send reported to HeldTooLongFunc, until none is
	// held (HeldEndedFunc).
	reportedFor map[string]AutomatedInput
	notes       notifier
	// gens counts DropHeld calls per session (AutomatedInput.gen).
	gens map[string]uint64
	// shadowLast: each held send's last logged screen and signal decisions (shadow
	// mode logs a send's decisions only when they change).
	shadowLast map[string]string
	// delivered: when each session's sends were last typed, by Key (LastDelivered).
	delivered map[string]map[string]time.Time
	// lastPasted: each session's last text Coral pasted, squeezed, until its Enter is seen to
	// submit it (#4104): left in the box (a dialog took the Enter), it is Coral's, not a draft.
	lastPasted map[string]string
	// dialogHeld: sessions whose sends were last held behind a dialog: the held notice stays
	// up (checkEnded) until a send is delivered again.
	dialogHeld map[string]bool
}

// Defaults for gateTimings' dialog fields.
const (
	defaultDialogReport        = 10 * time.Second
	defaultDialogMentionGiveUp = 10 * time.Minute
)

// WhyDialogGaveUp starts the reason a mention held behind a dialog was given up (#4104).
const WhyDialogGaveUp = "a dialog was open in the agent's terminal for"

// WhyEnterGaveUp is the reason a paste was given up because its Enter never submitted it, with no
// dialog recognised (#4104): most likely one Coral doesn't know.
const WhyEnterGaveUp = "its Enter didn't submit it, three times"

func (g *inputGate) dialogReportAfter() time.Duration {
	if g.timings.dialogReport > 0 {
		return g.timings.dialogReport
	}
	return defaultDialogReport
}

func (g *inputGate) dialogGiveUpAfter() time.Duration {
	if g.timings.dialogMentionGiveUp > 0 {
		return g.timings.dialogMentionGiveUp
	}
	return defaultDialogMentionGiveUp
}

// setLastPasted records (or, with "", forgets) the text Coral last pasted for name.
func (g *inputGate) setLastPasted(name, text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastPasted == nil {
		g.lastPasted = map[string]string{}
	}
	if text == "" {
		delete(g.lastPasted, name)
		return
	}
	g.lastPasted[name] = squeeze(text)
}

func (g *inputGate) lastPastedFor(name string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastPasted[name]
}

// noteDialogLocked records whether name's latest hold was behind a dialog (g.mu held).
func (g *inputGate) noteDialogLocked(name, kind string) {
	if kind == KindDialog {
		if g.dialogHeld == nil {
			g.dialogHeld = map[string]bool{}
		}
		g.dialogHeld[name] = true
	}
}

var gate = &inputGate{held: map[string][]*heldInput{}, reportedFor: map[string]AutomatedInput{}, gens: map[string]uint64{}, shadowLast: map[string]string{}, delivered: map[string]map[string]time.Time{}, now: time.Now,
	timings: gateTimings{tooLong: 2 * time.Minute, expiry: time.Hour, every: 2 * time.Second}}

func (g *inputGate) lockSession(name string) func() {
	m, _ := g.sending.LoadOrStore(name, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (g *inputGate) hold(in AutomatedInput, why, kind string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.staleLocked(in) {
		return
	}
	q := g.held[in.Name]
	for i, h := range q {
		if h.in.Key == in.Key {
			// Superseded: the newer send replaces it, in its place.
			q[i] = &heldInput{in: in, heldAt: h.heldAt, why: why, kind: kind, reported: h.reported, reportedPrompt: h.reportedPrompt}
			g.held[in.Name] = q
			g.noteDialogLocked(in.Name, kind)
			g.logHeld(in, why, "replaced a held send")
			return
		}
	}
	if len(q) >= heldPerSession {
		slog.Warn("automated input: too many held sends; dropping the oldest", "session", in.Name, "dropped", q[0].in.What)
		q = q[1:]
	}
	// And at most heldBytesPerSession of text (#3578: in signals mode a long turn holds every
	// routine send for it). Never the new one; the log names the sender, never the text.
	total := len(in.Text)
	for _, h := range q {
		total += len(h.in.Text)
	}
	for len(q) > 0 && total > heldBytesPerSession {
		slog.Warn("automated input: held sends over the size limit; dropping the oldest", "session", in.Name, "dropped", q[0].in.What)
		total -= len(q[0].in.Text)
		q = q[1:]
	}
	g.held[in.Name] = append(q, &heldInput{in: in, heldAt: g.now(), why: why, kind: kind})
	g.noteDialogLocked(in.Name, kind)
	g.logHeld(in, why, "held")
	if !g.running {
		g.running = true
		go func() { defer safego.Recover("ptymanager/automated_input:g.loop"); g.loop() }()
	}
}

func (g *inputGate) logHeld(in AutomatedInput, why, what string) {
	slog.Info("automated input "+what, "session", in.Name, "sender", in.What, "why", why)
	inputlog.Record(in.Name, inputlog.Text, "[HELD, "+why+"] "+in.Text)
}

// flush types what is held for name if the agent is no longer waiting. It
// reports whether something is still held (the agent is waiting again).
func (g *inputGate) flush(ctx context.Context, name string) bool {
	still := g.flushQueue(ctx, name)
	g.checkEnded(name)
	return still
}

// staleLocked reports (and logs) a send that reached the gate before DropHeld last
// ran for its session: it belongs to the old process and is dropped, not held.
func (g *inputGate) staleLocked(in AutomatedInput) bool {
	if in.gen == g.gens[in.Name] {
		return false
	}
	slog.Info("automated input: dropped a send for an agent that was restarted or killed", "session", in.Name, "sender", in.What)
	return true
}

// checkEnded calls HeldEndedFunc when name had a reported send and has none held now.
func (g *inputGate) checkEnded(name string) {
	g.mu.Lock()
	if g.dialogHeld[name] {
		// The dialog's notice stays until a send is delivered again (#4104): a mention given up
		// behind it empties the queue, but the dialog is still open.
		g.mu.Unlock()
		return
	}
	in, ok := g.reportedFor[name]
	if ok {
		for _, h := range g.held[name] {
			if h.reported {
				g.mu.Unlock()
				return
			}
		}
		delete(g.reportedFor, name)
	}
	g.mu.Unlock()
	if !ok {
		return
	}
	heldEnded.RLock()
	fn := heldEnded.fn
	heldEnded.RUnlock()
	if fn != nil {
		g.notes.post(func() { fn(in.Name, in.AgentType, in.SessionID) })
	}
}

func (g *inputGate) flushQueue(ctx context.Context, name string) bool {
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
		if in.heldSince.IsZero() {
			in.heldSince = h.heldAt // waiting's in-turn fallback counts from it when the turn's start is unknown
		}
		if in.RequireInputBox && g.now().Sub(h.heldAt) > g.timings.tooLong {
			in.RequireInputBox = false
		}
		g.mu.Unlock()
		if waiting, why, kind := in.waiting(ctx); waiting {
			if kind == KindDialog && g.giveUpMentionsBehindDialog(name) {
				continue // the head was a mention given up: look at the next
			}
			// Keep the current reason: a send held for a busy footer or a draft
			// that a real prompt now holds must say so (QA, after #3478).
			g.mu.Lock()
			changed := h.why != why
			h.why, h.kind = why, kind
			g.noteDialogLocked(name, kind)
			g.mu.Unlock()
			if changed {
				g.logHeld(h.in, why, "still held")
			}
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
		send := h.in
		send.heldSince, send.heldWhy, send.heldReported, send.heldReportedPrompt = h.heldAt, h.why, h.reported, h.reportedPrompt
		send.urgent = mentionWaiting(ctx)
		for _, behind := range g.held[name] {
			send.urgent = send.urgent || behind.in.Mention
		}
		g.mu.Unlock()
		if enterHeld, err := g.sendChecked(ctx, send); enterHeld {
			return true
		} else if errors.Is(err, ErrTeamBusy) {
			// Not now (#3613): back at the front, for the loop's next pass.
			g.mu.Lock()
			h.why, h.kind = whyTeamSpaced, ""
			g.held[name] = append([]*heldInput{h}, g.held[name]...)
			g.mu.Unlock()
			return true
		} else if err != nil {
			logHeldSendFailure(name, h.in.What, err)
		} else {
			g.shadowForget(name, h.in.Key)
			g.noteDelivered(name, h.in.Key)
			slog.Info("automated input delivered after the hold ended", "session", name, "sender", h.in.What, "held_for", g.now().Sub(h.heldAt).Round(time.Second).String(), "released", releasedBecause(h.why))
		}
	}
}

// giveUpMentionsBehindDialog gives up every mention held for name behind a dialog for
// dialogGiveUpAfter (#4104): never pasted, never typed into the dialog, and reported (the note
// says why). It reports whether the queue's head was one of them.
func (g *inputGate) giveUpMentionsBehindDialog(name string) (headGone bool) {
	limit := g.dialogGiveUpAfter()
	g.mu.Lock()
	q := g.held[name]
	var gone []AutomatedInput
	kept := q[:0:0]
	for i, h := range q {
		if h.in.Mention && g.now().Sub(h.heldAt) >= limit {
			in := h.in
			in.heldSince = h.heldAt
			gone = append(gone, in)
			if i == 0 {
				headGone = true
			}
			continue
		}
		kept = append(kept, h)
	}
	if len(gone) > 0 {
		g.held[name] = kept
	}
	g.mu.Unlock()
	for _, in := range gone {
		_ = g.giveUpPaste(in, fmt.Sprintf("%s %s", WhyDialogGaveUp, limit.Round(time.Minute)))
	}
	return headGone
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
			// One session's panic must not stop the loop, or held input would stay held for every
			// session (running stays true) (#3998).
			func() {
				defer safego.Recover("automated input: flush " + n)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				unlock := g.lockSession(n)
				defer unlock()
				g.flush(ctx, n)
			}()
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
			after := g.timings.tooLong
			if h.kind == KindDialog && g.dialogReportAfter() < after {
				after = g.dialogReportAfter() // something to dismiss now (#4104)
			}
			if (!h.reported || (h.kind != "" && !h.reportedPrompt)) && now.Sub(h.heldAt) > after && report != nil {
				h.reported = true
				h.reportedPrompt = h.reportedPrompt || h.kind != ""
				g.reportedFor[name] = h.in
				in, kind := h.in, h.kind
				g.notes.post(func() { report(in.Name, in.AgentType, in.SessionID, in.What, kind) })
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
	gate.gens[name]++
	gate.shadowForgetSessionLocked(name)
	delete(gate.delivered, name)
	delete(gate.lastPasted, name)
	delete(gate.dialogHeld, name)
	gate.mu.Unlock()
	noteDisagreement(name, "", time.Now()) // its disagreement window, if any, ends with it (#3655)
	gate.checkEnded(name)
	forgetSessionMemos(name)
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

// sessionEndHooks clear what other packages remember per session (a log line already
// written for it, and the like) when DropHeld ends that session's state, so those memos
// don't grow with every session the server has ever run (#4057).
var (
	sessionEndMu    sync.Mutex
	sessionEndHooks []func(name string)
)

// OnSessionEnd registers f to be called with a session's terminal name ("<type>-<id>")
// whenever its agent is killed, restarted, resumed or launched again (DropHeld). Register
// once, at package init: hooks are never removed.
func OnSessionEnd(f func(name string)) {
	sessionEndMu.Lock()
	defer sessionEndMu.Unlock()
	sessionEndHooks = append(sessionEndHooks, f)
}

func forgetSessionMemos(name string) {
	tmux.ForgetBareNameWarning(name)
	sessionEndMu.Lock()
	hooks := append([]func(string){}, sessionEndHooks...)
	sessionEndMu.Unlock()
	for _, f := range hooks {
		f(name)
	}
}

// HeldCount is the number of sends held for a session (tests and diagnostics).
func HeldCount(name string) int {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return len(gate.held[name])
}

// deliveredKeysPerSession bounds LastDelivered's record for one session.
const deliveredKeysPerSession = 64

// noteDelivered records that a send with key was just typed into name (LastDelivered).
func (g *inputGate) noteDelivered(name, key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.dialogHeld, name) // delivered: no dialog in the way any more (#4104)
	m := g.delivered[name]
	if m == nil {
		m = map[string]time.Time{}
		g.delivered[name] = m
	}
	if _, ok := m[key]; !ok && len(m) >= deliveredKeysPerSession {
		oldest, at := "", time.Time{}
		for k, t := range m {
			if oldest == "" || t.Before(at) {
				oldest, at = k, t
			}
		}
		delete(m, oldest)
	}
	m[key] = g.now()
}

// LastDelivered returns when a send with key was last typed into the session name, or
// the zero time if never: recorded when the gate actually writes it (at once, or on a
// held send's turn), never for a send that is held, handed off (TakeHeld) or dropped;
// DropHeld (the session ended or restarted) forgets it. The turn-end hand-off uses it
// to stand down when a nudge was typed during the turn, so it is already queued in the
// agent's input (#3510).
func LastDelivered(name, key string) time.Time {
	name = sessionOfTarget(name)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.delivered[name][key]
}

// heldSendFailures coalesces the held-send failure warning (#4112 part 2, Security's #4107 audit:
// 100 failed sends to a pane that was gone gave a warning each): the same session, sender and
// error log once, then once every 5 minutes with how many were held back.
var heldSendFailures = logdedupe.New(5 * time.Minute)

func logHeldSendFailure(name, what string, err error) {
	ok, heldBack := heldSendFailures.Allow(name + "\x00" + what + "\x00" + err.Error())
	if !ok {
		return
	}
	if heldBack > 0 {
		slog.Warn("automated input: a held send failed", "session", name, "sender", what, "error", err, "more_like_it", heldBack, "in_the_last", "5m")
		return
	}
	slog.Warn("automated input: a held send failed", "session", name, "sender", what, "error", err)
}
