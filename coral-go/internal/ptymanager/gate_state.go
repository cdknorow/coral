package ptymanager

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
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

// signalsMode reports whether the gate follows the signal rule (CORAL_GATE_MODE=
// signals); otherwise it follows the screen rule and logs the signal rule's decision.
func signalsMode() bool { return os.Getenv("CORAL_GATE_MODE") == "signals" }

// staleInTurn: an InTurn state with no event for this long is stale (2.2.4).
var staleInTurn = 2 * time.Minute

// HumanInputReader is implemented by terminals that see every keystroke a person
// sends to the agent (Coral's own PTYs): whether one arrived after t.
type HumanInputReader interface {
	HumanInputSince(name string, t time.Time) bool
}

// claudeBusyRe: Claude's working line ("✻ Thinking… (12s · esc to interrupt)").
var claudeBusyRe = regexp.MustCompile(`(?i)esc to interrupt`)

// signalWaiting is the signal rule (design 2.1 and 2.2): whether the send must wait,
// why, and whether it is a request to the user. ok is false when the session has no
// signals, and the screen rule decides alone.
func (in AutomatedInput) signalWaiting(ctx context.Context) (waiting bool, why string, prompt, ok bool) {
	stateFunc.RLock()
	fn := stateFunc.fn
	stateFunc.RUnlock()
	if fn == nil {
		return false, "", false, false
	}
	agentType, sessionID := in.identity()
	st, ok := fn(ctx, in.Name, agentType, sessionID)
	if !ok {
		return false, "", false, false
	}
	var screen string
	haveScreen := false
	if r, isReader := in.Term.(ScreenReader); isReader {
		if s, err := r.AgentScreen(ctx, in.Name, agentType, sessionID); err == nil {
			screen, haveScreen = s, true
		}
	}
	switch st.State {
	case StateGone:
		return true, "signals: the agent's session ended", false, true
	case StateStarting:
		// No hooks run before a trust screen is answered: the screen says whether
		// the launch waits on the user (Blocked{trust} in the design; #3484's corpus).
		if haveScreen {
			if open, why := PromptOnScreen(screen); open && isPromptHold(why) {
				return true, "signals: starting, screen: " + why, true, true
			}
		}
		return true, "signals: starting", false, true
	case StateBlocked:
		return true, "signals: blocked (" + st.Kind + ")", true, true
	case StateInTurn:
		// agy and pi signal no prompt: the screen is read for one only while a tool
		// call is pending (2.2.3), so a busy footer is never a prompt.
		if haveScreen && st.ToolPending && !hasPromptSignal(agentType) {
			if open, why := PromptOnScreen(screen); open && isPromptHold(why) {
				return true, "signals: in turn, tool pending, screen: " + why, true, true
			}
		}
		if in.staleRelaxation(st, agentType, screen, haveScreen) {
			slog.Info("automated input: a stale in-turn state is taken as idle (the screen shows Claude's empty box)",
				"session", in.Name, "last_event", st.LastEvent.Format(time.RFC3339), "cli_version", st.CLIVersion)
			break
		}
		return true, "signals: in turn", false, true
	case StateIdle:
	default:
		return true, "signals: unknown state", false, true
	}
	// Idle by signals: the screen may still hold more.
	if haveScreen {
		if open, why := PromptOnScreen(screen); open {
			if isPromptHold(why) {
				slog.Warn("automated input: state_disagreement: the signals say idle but the screen shows a prompt",
					"session", in.Name, "why", why, "cli_version", st.CLIVersion)
			}
			return true, "signals: idle, screen: " + why, isPromptHold(why), true
		}
	}
	if in.RequireInputBox && (agentType == at.Claude || agentType == at.Codex) && (!haveScreen || !InputBoxOnScreen(screen)) {
		return true, "signals: idle, the agent's input box isn't on screen yet", false, true
	}
	if hr, isPTY := in.Term.(HumanInputReader); isPTY && !st.LastStop.IsZero() && hr.HumanInputSince(in.Name, st.LastStop) {
		// The PTY's box can't be read; a person who typed since the turn ended has a
		// draft by definition (2.2.6, P3).
		return true, whyDraft, false, true
	}
	return false, "", false, true
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

// shadowLog records one decision's screen rule and signal rule, once per change for a
// send (shadow mode, phase 1).
func (g *inputGate) shadowLog(in AutomatedInput, oldWait bool, oldWhy string, newWait bool, newWhy string) {
	decision := func(wait bool, why string) string {
		if wait {
			return "hold: " + why
		}
		return "send"
	}
	old, nw := decision(oldWait, oldWhy), decision(newWait, newWhy)
	k := in.Name + "\x00" + in.Key
	g.mu.Lock()
	if g.shadowLast[k] == old+"\x00"+nw {
		g.mu.Unlock()
		return
	}
	g.shadowLast[k] = old + "\x00" + nw
	g.mu.Unlock()
	mode := "screen"
	if signalsMode() {
		mode = "signals"
	}
	slog.Info("automated input decision", "session", in.Name, "sender", in.What, "key", in.Key,
		"old", old, "new", nw, "agree", oldWait == newWait, "mode", mode)
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

// TakeHeld retires the held sends of a session whose Key matches, for a channel that
// delivers them another way (the turn-end hand-off, 3A): exactly once across
// channels, none of them is typed afterwards. It waits for a send in progress to the
// session (the gate's per-session lock) until ctx is done, and then takes nothing.
func TakeHeld(ctx context.Context, name string, match func(key string) bool) []TakenSend {
	name = sessionOfTarget(name)
	unlock, ok := gate.lockSessionCtx(ctx, name)
	if !ok {
		return nil
	}
	defer unlock()
	gate.mu.Lock()
	var taken []TakenSend
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
		slog.Info("automated input handed off", "session", name, "sender", t.What, "key", t.Key)
	}
	return taken
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
