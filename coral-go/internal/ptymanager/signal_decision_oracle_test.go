package ptymanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// signalDecision, split into reading the screen, the decision for each state but idle, and idle's checks (#4713),
// decides as signalDecisionBefore4713 (the function at v1.4.92, below) does: whether a send waits, why, on what
// kind of request, the state it decided on, and the disagreements it notes. Each case runs both from the same
// disagreement record. Each reason has a floor, so none goes unexercised.

type sdTerm struct {
	screen    string
	screenErr bool
	typed     bool // a person typed since the turn ended (HumanInputReader)
}

func (s sdTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	if s.screenErr {
		return s.screen, errors.New("capture failed") // what was read before it failed: never used
	}
	return s.screen, nil
}

type sdPTY struct{ sdTerm }

func (p sdPTY) HumanInputSince(string, string, time.Time) bool { return p.typed }

var sdTrust = func() string {
	b, err := os.ReadFile("testdata/screens/claude/2.1.300/trust.ansi")
	if err != nil {
		panic(err)
	}
	// A Windows checkout writes the capture with CRLF line ends: a screen line is read without its \r, as on a terminal.
	return sgrSeq.ReplaceAllString(strings.ReplaceAll(string(b), "\r\n", "\n"), "")
}()

func sdScreens() []string {
	return []string{pcBox(""), pcBox("a draft"), permissionMenu, sdTrust, pcDialog, "just output\n",
		"✻ Thinking… (12s · esc to interrupt)\n" + pcBox(""), pcBox("Try \"fix typecheck errors\""),
		"Reading the files…\n Esc to cancel · Tab to amend\n"} // a prompt's footer alone: open, but not a request
}

func disagreementWhys() string {
	disagreements.Lock()
	defer disagreements.Unlock()
	var out []string
	for k, v := range disagreements.m {
		out = append(out, k+"="+v.why)
	}
	sort.Strings(out)
	return strings.Join(out, ";")
}

func setDisagreements(m map[string]disagreement) {
	disagreements.Lock()
	defer disagreements.Unlock()
	disagreements.m = map[string]disagreement{}
	for k, v := range m {
		disagreements.m[k] = v
	}
}

func copyDisagreements() map[string]disagreement {
	disagreements.Lock()
	defer disagreements.Unlock()
	out := map[string]disagreement{}
	for k, v := range disagreements.m {
		out[k] = v
	}
	return out
}

func TestSignalDecisionAgreesWithBefore4713(t *testing.T) {
	stateFunc.RLock()
	prevFn := stateFunc.fn
	stateFunc.RUnlock()
	prevD := copyDisagreements()
	prevLog := slog.Default()
	t.Cleanup(func() { SetStateFunc(prevFn); setDisagreements(prevD); slog.SetDefault(prevLog) })
	slog.SetDefault(slog.New(slog.NewTextHandler(nopWriter{}, nil))) // the stale and disagreement lines
	screens := sdScreens()
	reasons := map[string]int{}
	now := time.Now()
	for seed := int64(1); seed <= 10000; seed++ {
		rng := rand.New(rand.NewSource(seed))
		st := AgentState{
			State:       []TurnState{StateUnknown, StateStarting, StateIdle, StateIdle, StateIdle, StateInTurn, StateInTurn, StateBlocked, StateGone, TurnState(42)}[rng.Intn(10)],
			Kind:        []string{"", "permission", "question"}[rng.Intn(3)],
			ToolPending: rng.Intn(2) == 0,
			CLIVersion:  "2.1.300",
		}
		st.LastEvent = []time.Time{{}, now.Add(-10 * time.Second), now.Add(-10 * time.Minute)}[rng.Intn(3)]
		if rng.Intn(2) == 0 {
			st.LastStop = now.Add(-time.Minute)
		}
		haveState := rng.Intn(12) > 0
		noFn := rng.Intn(25) == 0
		term := sdTerm{screen: screens[rng.Intn(len(screens))], screenErr: rng.Intn(10) == 0, typed: rng.Intn(2) == 0}
		var tm any = term
		switch rng.Intn(5) {
		case 0:
			tm = sdPTY{term}
		case 1:
			tm = struct{}{} // can't show its screen
		}
		in := AutomatedInput{Term: tm, Name: fmt.Sprintf("sd-%d", rng.Intn(3)), AgentType: []string{at.Claude, at.Claude, at.Codex, at.Agy, "pi", at.Gemini}[rng.Intn(6)],
			SessionID: "sd-1", RequireInputBox: rng.Intn(3) == 0}
		if noFn {
			SetStateFunc(nil)
		} else {
			SetStateFunc(func(context.Context, string, string, string) (AgentState, bool) { return st, haveState })
		}
		if rng.Intn(4) == 0 { // a window already open for the session
			setDisagreements(map[string]disagreement{in.Name: {why: "an earlier prompt", seen: time.Now()}})
		}
		before := copyDisagreements()
		w1, why1, k1, st1, ok1 := in.signalDecisionBefore4713(context.Background())
		after1 := disagreementWhys()
		setDisagreements(before)
		w2, why2, k2, st2, ok2 := in.signalDecision(context.Background())
		after2 := disagreementWhys()
		got := fmt.Sprintf("waiting=%v why=%q kind=%q st=%+v ok=%v disagreements=%s", w2, why2, k2, st2, ok2, after2)
		require.Equal(t, fmt.Sprintf("waiting=%v why=%q kind=%q st=%+v ok=%v disagreements=%s", w1, why1, k1, st1, ok1, after1), got, "seed %d: %+v", seed, in)
		reason := why2
		if i := strings.Index(reason, "screen: "); i >= 0 {
			reason = reason[:i] + "screen"
		}
		switch {
		case !ok2:
			reason = "no signals"
		case !w2 && st2.State == StateInTurn:
			reason = "a stale turn taken as idle"
		case !w2:
			reason = "sent"
		}
		if after2 != "" && strings.Contains(after2, in.Name+"=") && why2 != "" && strings.HasPrefix(why2, "signals: idle, screen") {
			if k2 == kindPrompt {
				reason += " (a disagreement)"
			}
		}
		reasons[reason]++
	}
	t.Logf("compared, per reason: %v", reasons)
	for _, r := range []string{"no signals", "sent", "a stale turn taken as idle", "signals: the agent's session ended", "signals: starting",
		"signals: starting, screen", "signals: blocked (permission)", "signals: blocked ()", "signals: in turn", "signals: in turn, tool pending, screen",
		"signals: unknown state", "signals: idle, screen", "signals: idle, screen (a disagreement)",
		"signals: idle, the agent's input box isn't on screen yet", whyDraft} {
		assert.GreaterOrEqual(t, reasons[r], 15, "%q went (almost) unexercised", r)
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// signalDecisionBefore4713 is signalDecision before #4713 split it (gate_state.go at v1.4.92), kept to compare with.
func (in AutomatedInput) signalDecisionBefore4713(ctx context.Context) (waiting bool, why, kind string, st AgentState, ok bool) {
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
	var screen string
	haveScreen := false
	if r, isReader := in.Term.(ScreenReader); isReader {
		if s, err := r.AgentScreen(ctx, in.Name, agentType, sessionID); err == nil {
			screen, haveScreen = s, true
		}
	}
	switch st.State {
	case StateGone:
		return true, "signals: the agent's session ended", "", st, true
	case StateStarting:
		// No hooks run before a trust screen is answered: the screen says whether
		// the launch waits on the user (Blocked{trust} in the design; #3484's corpus).
		if haveScreen {
			if open, why := PromptOnScreen(screen); open && isPromptHold(why) {
				return true, "signals: starting, screen: " + why, kindTrust, st, true
			}
		}
		return true, "signals: starting", "", st, true
	case StateBlocked:
		return true, "signals: blocked (" + st.Kind + ")", blockedKind(st.Kind), st, true
	case StateInTurn:
		// agy and pi signal no prompt: the screen is read for one only while a tool
		// call is pending (2.2.3), so a busy footer is never a prompt.
		if haveScreen && st.ToolPending && !hasPromptSignal(agentType) {
			if open, why := PromptOnScreen(screen); open && isPromptHold(why) {
				return true, "signals: in turn, tool pending, screen: " + why, kindPrompt, st, true
			}
		}
		if in.staleRelaxation(st, agentType, screen, haveScreen) {
			slog.Info("automated input: a stale in-turn state is taken as idle (the screen shows Claude's empty box)",
				"session", in.Name, "last_event", st.LastEvent.Format(time.RFC3339), "cli_version", st.CLIVersion)
			break
		}
		return true, "signals: in turn", "", st, true
	case StateIdle:
	default:
		return true, "signals: unknown state", "", st, true
	}
	// Idle by signals: the screen may still hold more.
	if haveScreen {
		if open, why := PromptOnScreen(screen); open {
			if isPromptHold(why) && !isDialogHold(why) { // a dialog isn't a request the signals would show
				disagreed = true
				if noteDisagreement(in.Name, why, time.Now()) {
					slog.Warn("automated input: state_disagreement: the signals say idle but the screen shows a prompt",
						"session", in.Name, "why", why, "cli_version", st.CLIVersion)
				}
			}
			return true, "signals: idle, screen: " + why, promptKind(why), st, true
		}
	}
	if in.RequireInputBox && (agentType == at.Claude || agentType == at.Codex) && (!haveScreen || !InputBoxOnScreen(screen)) {
		return true, "signals: idle, the agent's input box isn't on screen yet", "", st, true
	}
	if hr, isPTY := in.Term.(HumanInputReader); isPTY && !st.LastStop.IsZero() && hr.HumanInputSince(in.Name, in.SessionID, st.LastStop) {
		// The PTY's box can't be read; a person who typed since the turn ended has a
		// draft by definition (2.2.6, P3).
		return true, whyDraft, "", st, true
	}
	return false, "", "", st, true
}
