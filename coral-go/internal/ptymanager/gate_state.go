package ptymanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// The gate on the CLIs' own signals (#3475 design v2.1, #3482).
//
// The server keeps one turn state per session from hook events and transcript
// end-of-turn records (Lead's side) and hands it to the gate through StateFunc. In
// phase 1 the gate only logs what the signal rule would decide next to today's
// screen rule (shadow mode, one line per change); CORAL_GATE_MODE=signals makes it
// follow the signal rule. The screen stays a hold-more input either way: the draft
// check and the before-Enter re-check run in both modes.

// TurnState is an agent's turn state from its hook events.
type TurnState int

const (
	StateUnknown TurnState = iota
	StateStarting
	StateIdle
	StateInTurn
	StateBlocked
	StateGone
)

func (s TurnState) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateIdle:
		return "idle"
	case StateInTurn:
		return "in_turn"
	case StateBlocked:
		return "blocked"
	case StateGone:
		return "gone"
	}
	return "unknown"
}

// AgentState is what the server knows about a session from its signals.
type AgentState struct {
	State TurnState
	// Kind is why it is Blocked: permission, question, plan or elicitation.
	Kind string
	// Since is when it entered State; LastEvent is the session's latest event.
	Since, LastEvent time.Time
	// LastStop is the latest end of a turn (Stop, Interrupt or a transcript record),
	// for the PTY rule (no person typed since).
	LastStop time.Time
	// ToolPending: a tool call started and hasn't finished (agy and pi, which have no
	// prompt signal: a prompt can only be open in that window).
	ToolPending bool
	CLIVersion  string
}

// StateFunc returns a session's state from its signals; ok is false when there are
// none (no hooks installed for it, or none received yet).
var stateFunc struct {
	sync.RWMutex
	fn func(ctx context.Context, name, agentType, sessionID string) (AgentState, bool)
}

// SetStateFunc installs the state source for the gate.
func SetStateFunc(fn func(ctx context.Context, name, agentType, sessionID string) (AgentState, bool)) {
	stateFunc.Lock()
	stateFunc.fn = fn
	stateFunc.Unlock()
}

// gateFollowsSignals reports whether the gate decides by the signal rule for an agent type
// (phase 2, #3578); otherwise it follows the screen rule and logs the signal rule's decision
// (shadow mode). CORAL_GATE_MODE=screen never (the kill switch); =signals always, for every CLI
// (trials); otherwise Claude, when the Operator's gate_signals_claude setting is on (off by
// default). Codex, Gemini and the others keep the screen rule until their signals are in (#3497).
func gateFollowsSignals(agentType string) bool {
	switch os.Getenv("CORAL_GATE_MODE") {
	case "screen":
		return false
	case "signals":
		return true
	}
	if agentType != at.Claude {
		return false
	}
	signalsSetting.RLock()
	fn := signalsSetting.fn
	signalsSetting.RUnlock()
	return fn != nil && fn()
}

// signalsSetting reads the Operator's gate_signals_claude setting (installed by the server).
var signalsSetting struct {
	sync.RWMutex
	fn func() bool
}

// SetSignalsSettingFunc installs the reader of the gate_signals_claude setting.
func SetSignalsSettingFunc(fn func() bool) {
	signalsSetting.Lock()
	signalsSetting.fn = fn
	signalsSetting.Unlock()
}

// inTurnFallback: after this long in one turn, a send the signal rule would hold for the turn
// follows the screen rule instead, so nothing waits on a turn for ever (#3578; the longest real
// turn in the shadow week's first day was 36.5 min).
var inTurnFallback = 30 * time.Minute

// staleInTurn: an InTurn state with no event for this long is stale (2.2.4).
var staleInTurn = 2 * time.Minute

// HumanInputReader is implemented by terminals that see every keystroke a person
// sends to the agent (Coral's own PTYs): whether one arrived after t. The terminal is the
// agent's own, by sessionID when given (#3704: never another agent with the same name).
type HumanInputReader interface {
	HumanInputSince(name, sessionID string, t time.Time) bool
}

// claudeBusyRe: Claude's working line ("✻ Thinking… (12s · esc to interrupt)").
var claudeBusyRe = regexp.MustCompile(`(?i)esc to interrupt`)

// signalWaiting is the signal rule (design 2.1 and 2.2): whether the send must wait,
// why, and what it waits on when it is a request to the user (holdKind). ok is false when the session has no
// signals, and the screen rule decides alone.
func (in AutomatedInput) signalWaiting(ctx context.Context) (waiting bool, why, kind string, ok bool) {
	waiting, why, kind, _, ok = in.signalDecision(ctx)
	return waiting, why, kind, ok
}

// signalDecision is signalWaiting, also returning the state it decided on (for the
// shadow log).
func (in AutomatedInput) signalDecision(ctx context.Context) (waiting bool, why, kind string, st AgentState, ok bool) {
	stateFunc.RLock()
	fn := stateFunc.fn
	stateFunc.RUnlock()
	if fn == nil {
		return false, "", "", st, false
	}
	agentType, sessionID := in.identity()
	st, ok = fn(ctx, in.Name, agentType, sessionID)
	if !ok {
		return false, "", "", st, false
	}
	disagreed := false
	defer func() {
		if !disagreed {
			noteDisagreement(in.Name, "", time.Now()) // any other decision ends the session's window
		}
	}()
	screen, haveScreen := in.signalScreen(ctx, agentType, sessionID)
	if waiting, why, kind, decided := in.stateDecision(st, agentType, screen, haveScreen); decided {
		return waiting, why, kind, st, true
	}
	waiting, why, kind, disagreed = in.idleDecision(st, agentType, screen, haveScreen)
	return waiting, why, kind, st, true
}

// signalsSayEnded reports whether the agent's OWN signals say its turn has ended: it is idle by them, with a Stop seen and no request of its own open (#6466). It reads the state alone (no screen,
// no disagreement log): the caller is about to decide what a screen that cannot show the input box means, and an agent that has stopped is not one that is mid-turn.
func (in AutomatedInput) signalsSayEnded(ctx context.Context) bool {
	stateFunc.RLock()
	fn := stateFunc.fn
	stateFunc.RUnlock()
	if fn == nil {
		return false
	}
	agentType, sessionID := in.identity()
	st, ok := fn(ctx, in.Name, agentType, sessionID)
	return ok && st.State == StateIdle && !st.LastStop.IsZero()
}

// signalScreen is the agent's screen, when its terminal can show it.
func (in AutomatedInput) signalScreen(ctx context.Context, agentType, sessionID string) (screen string, ok bool) {
	if r, isReader := in.Term.(ScreenReader); isReader {
		if s, err := r.AgentScreen(ctx, in.Name, agentType, sessionID); err == nil {
			return s, true
		}
	}
	return "", false
}

// stateDecision decides for every state but idle: gone, starting (a trust screen is the user's), blocked, in turn
// (a pending tool's prompt on screen for agents that signal none), unknown. decided is false for idle, and for a
// stale in-turn state 2.2.4 takes as idle: the idle checks then decide.
func (in AutomatedInput) stateDecision(st AgentState, agentType, screen string, haveScreen bool) (waiting bool, why, kind string, decided bool) {
	switch st.State {
	case StateGone:
		return true, "signals: the agent's session ended", "", true
	case StateStarting:
		// No hooks run before a trust screen is answered: the screen says whether
		// the launch waits on the user (Blocked{trust} in the design; #3484's corpus).
		if haveScreen {
			if open, why := PromptOnScreen(screen); open && isPromptHold(why) {
				return true, "signals: starting, screen: " + why, kindTrust, true
			}
		}
		return true, "signals: starting", "", true
	case StateBlocked:
		return true, "signals: blocked (" + st.Kind + ")", blockedKind(st.Kind), true
	case StateInTurn:
		// agy and pi signal no prompt: the screen is read for one only while a tool
		// call is pending (2.2.3), so a busy footer is never a prompt.
		if haveScreen && st.ToolPending && !hasPromptSignal(agentType) {
			if open, why := PromptOnScreen(screen); open && isPromptHold(why) {
				return true, "signals: in turn, tool pending, screen: " + why, kindPrompt, true
			}
		}
		if in.staleRelaxation(st, agentType, screen, haveScreen) {
			slog.Info("automated input: a stale in-turn state is taken as idle (the screen shows Claude's empty box)",
				"session", in.Name, "last_event", st.LastEvent.Format(time.RFC3339), "cli_version", st.CLIVersion)
			return false, "", "", false
		}
		return true, "signals: in turn", "", true
	case StateIdle:
		return false, "", "", false
	}
	return true, "signals: unknown state", "", true
}

// idleDecision is for an agent idle by its signals, where the screen may still hold more: a prompt (a request the
// signals didn't show is a disagreement, noted), no input box yet for a send that needs one, or a person's draft
// on a PTY that can't show its box.
func (in AutomatedInput) idleDecision(st AgentState, agentType, screen string, haveScreen bool) (waiting bool, why, kind string, disagreed bool) {
	if haveScreen {
		if open, why := PromptOnScreen(screen); open {
			if isPromptHold(why) && !isDialogHold(why) { // a dialog isn't a request the signals would show
				disagreed = true
				if noteDisagreement(in.Name, why, time.Now()) {
					slog.Warn("automated input: state_disagreement: the signals say idle but the screen shows a prompt",
						"session", in.Name, "why", why, "cli_version", st.CLIVersion)
				}
			}
			return true, "signals: idle, screen: " + why, promptKind(why), disagreed
		}
	}
	if in.RequireInputBox && (agentType == at.Claude || agentType == at.Codex) && (!haveScreen || !InputBoxOnScreen(screen)) {
		return true, "signals: idle, the agent's input box isn't on screen yet", "", false
	}
	if hr, isPTY := in.Term.(HumanInputReader); isPTY && !st.LastStop.IsZero() && hr.HumanInputSince(in.Name, in.SessionID, st.LastStop) {
		// The PTY's box can't be read; a person who typed since the turn ended has a
		// draft by definition (2.2.6, P3).
		return true, whyDraft, "", false
	}
	return false, "", "", false
}

// Hold kinds (HeldTooLongFunc): what a request to the user is. "" is a hold for the
// input box (a draft, no box on screen) or a busy agent: not a request.
const (
	kindPrompt = "prompt" // a prompt read from the screen
	kindTrust  = "trust"  // a trust screen at launch (no hooks run before it)
	// KindDialog: a dialog Claude Code draws over its input box (its bug-report draft, #4104).
	// Not a question for the agent: shown as "delivery held: a dialog is open".
	KindDialog = "dialog"
)

// blockedKind is the kind a Blocked state reports: the agent's own (permission,
// question, plan, elicitation), or a prompt when it didn't say.
func blockedKind(k string) string {
	if k == "" {
		return kindPrompt
	}
	return k
}

// promptKind is the kind for a reason PromptOnScreen gave: a prompt, or "" for a
// footer alone or a busy agent.
func promptKind(why string) string {
	if isDialogHold(why) {
		return KindDialog
	}
	if isPromptHold(why) {
		return kindPrompt
	}
	return ""
}

// hasPromptSignal reports whether the CLI signals its prompts (PermissionRequest).
func hasPromptSignal(agentType string) bool {
	return agentType == at.Claude || agentType == at.Codex
}

// staleRelaxation is 2.2.4's one bounded exception: Claude only, from InTurn only,
// no event for staleInTurn, and a screen that positively shows an empty input box
// with no prompt and no busy line (Claude's interrupt sends no hook).
func (in AutomatedInput) staleRelaxation(st AgentState, agentType, screen string, haveScreen bool) bool {
	if agentType != at.Claude || st.State != StateInTurn || !haveScreen || st.LastEvent.IsZero() || time.Since(st.LastEvent) < staleInTurn {
		return false
	}
	if open, _ := PromptOnScreen(screen); open || !InputBoxOnScreen(screen) {
		return false
	}
	for _, l := range tailLines(screenText(screen), promptScreenLines) {
		if claudeBusyRe.MatchString(l) || busyLineRe.MatchString(l) {
			return false
		}
	}
	box, ok := inputBoxText(screen)
	return ok && boxPlaceholder(box)
}

// disagreements are the sessions whose signals say idle while the screen shows a prompt, with
// the reason and when it was last seen (#3655 F4). A held send is re-checked every couple of
// seconds, so the state_disagreement line is logged once per window: when one opens, or its
// reason changes. A decision for the session that doesn't disagree ends the window, and so does
// a gap of disagreementGap (a held send dropped without another decision), so two separate
// windows are two lines and coral-shadow-report counts windows.
var disagreements struct {
	sync.Mutex
	m map[string]disagreement
}

type disagreement struct {
	why  string
	seen time.Time
}

// disagreementGap: a disagreement seen again after this long is a new window.
var disagreementGap = time.Minute

// noteDisagreement records a decision for session name at now: why is the disagreement's
// reason, or "" for none (the window, if any, ends). It reports whether a line is due.
func noteDisagreement(name, why string, now time.Time) bool {
	disagreements.Lock()
	defer disagreements.Unlock()
	if why == "" {
		delete(disagreements.m, name)
		return false
	}
	if disagreements.m == nil {
		disagreements.m = map[string]disagreement{}
	}
	prev, open := disagreements.m[name]
	disagreements.m[name] = disagreement{why: why, seen: now}
	return !open || prev.why != why || now.Sub(prev.seen) > disagreementGap
}

// shadowLog records one decision's screen rule and signal rule, once per change for a
// send (shadow mode, phase 1).
func (g *inputGate) shadowLog(in AutomatedInput, oldWait bool, oldWhy string, newWait bool, newWhy, kind string, st AgentState, decided string) {
	decision := func(wait bool, why string) string {
		if wait {
			return "hold: " + why
		}
		return "send"
	}
	old, nw := decision(oldWait, oldWhy), decision(newWait, newWhy)
	k := in.Name + "\x00" + in.Key
	g.mu.Lock()
	if g.shadowLast[k] == old+"\x00"+nw+"\x00"+decided {
		g.mu.Unlock()
		return
	}
	g.shadowLast[k] = old + "\x00" + nw + "\x00" + decided
	g.mu.Unlock()
	agentType, _ := in.identity()
	mode := "screen"
	if gateFollowsSignals(agentType) {
		mode = "signals"
	}
	// mode is the rule configured for the agent; decided is the one that decided this send (a
	// mention or a long turn goes by the screen rule even in signals mode).
	// state_for is how long the signals have been in their state (#3662 P6): for a hold with no
	// prompt on screen, a lower bound on how long the signal rule would hold the send.
	stateFor := ""
	if !st.Since.IsZero() {
		stateFor = time.Since(st.Since).Round(time.Second).String()
	}
	slog.Info("automated input decision", "session", in.Name, "sender", in.What, "key", logKey(in),
		"old", old, "new", nw, "agree", oldWait == newWait, "mode", mode, "decided", decided,
		"agent_type", agentType, "cli_version", st.CLIVersion, "state", st.State.String(), "kind", kind, "mention", in.Mention,
		"state_for", stateFor)
}

// logKey is a send's Key for a log line, never its text: a send without a key of its
// own is keyed by its text (a board message), which stays out of the server log.
func logKey(in AutomatedInput) string {
	if in.Key == "" || in.Key == in.Text {
		sum := sha256.Sum256([]byte(in.Key))
		return "text:" + hex.EncodeToString(sum[:4])
	}
	return in.Key
}

// shadowForget drops a send's shadow record once it is delivered, handed off or dropped.
func (g *inputGate) shadowForget(name, key string) {
	g.mu.Lock()
	delete(g.shadowLast, name+"\x00"+key)
	g.mu.Unlock()
}

// shadowForgetSession drops every shadow record of a session.
func (g *inputGate) shadowForgetSessionLocked(name string) {
	prefix := name + "\x00"
	for k := range g.shadowLast {
		if strings.HasPrefix(k, prefix) {
			delete(g.shadowLast, k)
		}
	}
}

// TakenSend is a held send retired by TakeHeld.
type TakenSend struct {
	Key, What, Text string
	HeldSince       time.Time
}

// TakeHeld retires the held sends of a session whose Key matches: for a channel that
// delivers them another way (the turn-end hand-off, 3A), exactly once across
// channels, or because they are stale (the notifier: the agent already read what a
// held nudge announces). None of them is typed afterwards; why goes into the log. It
// waits for a send in progress to the session (the gate's per-session lock) until ctx
// is done; ok is false then and nothing is taken.
func TakeHeld(ctx context.Context, name, why string, match func(key string) bool) (taken []TakenSend, ok bool) {
	name = sessionOfTarget(name)
	unlock, locked := gate.lockSessionCtx(ctx, name)
	if !locked {
		return nil, false
	}
	defer unlock()
	gate.mu.Lock()
	var kept []*heldInput
	for _, h := range gate.held[name] {
		if match(h.in.Key) {
			taken = append(taken, TakenSend{Key: h.in.Key, What: h.in.What, Text: h.in.Text, HeldSince: h.heldAt})
			gate.shadowForgetLocked(name, h.in.Key)
			continue
		}
		kept = append(kept, h)
	}
	if len(kept) == 0 {
		delete(gate.held, name)
	} else {
		gate.held[name] = kept
	}
	gate.mu.Unlock()
	gate.checkEnded(name)
	for _, t := range taken {
		slog.Info("automated input retired", "session", name, "sender", t.What, "key", logKey(AutomatedInput{Key: t.Key, Text: t.Text}), "why", why)
	}
	return taken, true
}

func (g *inputGate) shadowForgetLocked(name, key string) {
	delete(g.shadowLast, name+"\x00"+key)
}

// lockSessionCtx is lockSession that gives up when ctx is done.
func (g *inputGate) lockSessionCtx(ctx context.Context, name string) (func(), bool) {
	m, _ := g.sending.LoadOrStore(name, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	for {
		if mu.TryLock() {
			return mu.Unlock, true
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(5 * time.Millisecond):
		}
	}
}
