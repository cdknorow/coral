package ptymanager

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// withState installs a fixed signal state for every session and restores the gate. It returns the function that changes the state
// while the gate runs: the gate's goroutine reads the state under the same lock, so a test never writes it bare (#5064: the race
// detector caught `*st = ...` against the read).
func withState(t *testing.T, st AgentState, ok bool) (set func(AgentState)) {
	t.Helper()
	cur := st
	var mu sync.Mutex
	SetStateFunc(func(context.Context, string, string, string) (AgentState, bool) {
		mu.Lock()
		defer mu.Unlock()
		return cur, ok
	})
	t.Cleanup(func() { SetStateFunc(nil) })
	return func(next AgentState) {
		mu.Lock()
		defer mu.Unlock()
		cur = next
	}
}

// The signal rule (design 2.1/2.2), decided against a screen. Each case is what the
// gate would do in signals mode.
func TestSignalRule(t *testing.T) {
	fastGate(t)
	old := time.Now().Add(-10 * time.Minute)
	claudeIdle := "⏺ Done.\n\n❯ \n────────\n  ? for shortcuts\n"
	claudeWorking := "✻ Thinking… (12s · esc to interrupt)\n\n❯ \n────────\n"
	agyBusy := "⣷  Working...\n" + strings.Repeat("─", 40) + "\n>\n" + strings.Repeat("─", 40) + "\nesc to cancel      Gemini 3.8 Flash · high\n"
	agyMenu := "  Run this command?\n  > 1. Allow once\n    2. Deny\nesc to cancel      Gemini 3.8 Flash · high\n"
	for name, c := range map[string]struct {
		agent  string
		st     AgentState
		screen string
		wait   bool
		prompt bool
		why    string
	}{
		"idle, idle box":                   {"claude", AgentState{State: StateIdle}, claudeIdle, false, false, ""},
		"in turn":                          {"claude", AgentState{State: StateInTurn, LastEvent: time.Now()}, claudeWorking, true, false, "in turn"},
		"blocked: a request to the user":   {"claude", AgentState{State: StateBlocked, Kind: "permission"}, permissionMenu, true, true, "blocked (permission)"},
		"starting":                         {"claude", AgentState{State: StateStarting}, "", true, false, "starting"},
		"starting at a trust screen":       {"claude", AgentState{State: StateStarting}, " Quick safety check\n ❯ No, exit\n   Yes, I trust this folder\n\n Enter to confirm · Esc to cancel\n", true, true, "starting, screen: selection menu"},
		"gone":                             {"claude", AgentState{State: StateGone}, "", true, false, "ended"},
		"idle but a menu: disagreement":    {"claude", AgentState{State: StateIdle}, permissionMenu, true, true, "selection menu"},
		"stale, Claude, empty box: idle":   {"claude", AgentState{State: StateInTurn, LastEvent: old}, claudeIdle, false, false, ""},
		"stale, Claude, still working":     {"claude", AgentState{State: StateInTurn, LastEvent: old}, claudeWorking, true, false, "in turn"},
		"stale, agy: no relaxation":        {"agy", AgentState{State: StateInTurn, LastEvent: old}, agyBusy, true, false, "in turn"},
		"agy busy footer, no tool pending": {"agy", AgentState{State: StateInTurn, LastEvent: time.Now()}, agyBusy, true, false, "in turn"},
		"agy menu while a tool is pending": {"agy", AgentState{State: StateInTurn, LastEvent: time.Now(), ToolPending: true}, agyMenu, true, true, "tool pending"},
		"agy busy while a tool is pending": {"agy", AgentState{State: StateInTurn, LastEvent: time.Now(), ToolPending: true}, agyBusy, true, false, "in turn"},
	} {
		withState(t, c.st, true)
		a := &fakeAgent{screen: c.screen}
		in := a.input(c.agent+"-rule", "", "nudge", false)
		in.AgentType = c.agent
		wait, why, kind, ok := in.signalWaiting(context.Background())
		prompt := kind != ""
		require.True(t, ok, name)
		assert.Equal(t, c.wait, wait, "%s: %s", name, why)
		assert.Equal(t, c.prompt, prompt, "%s: %s", name, why)
		assert.Contains(t, why, c.why, name)
	}

	// The kinds HeldTooLongFunc reports.
	for _, c := range []struct {
		st     AgentState
		screen string
		kind   string
	}{
		{AgentState{State: StateBlocked, Kind: "plan"}, permissionMenu, "plan"},
		{AgentState{State: StateBlocked}, permissionMenu, kindPrompt},
		{AgentState{State: StateStarting}, " Quick safety check\n ❯ No, exit\n   Yes, I trust this folder\n\n Enter to confirm · Esc to cancel\n", kindTrust},
		{AgentState{State: StateIdle}, permissionMenu, kindPrompt},
		{AgentState{State: StateInTurn, LastEvent: time.Now()}, claudeWorking, ""},
	} {
		withState(t, c.st, true)
		_, why, kind, _ := (&fakeAgent{screen: c.screen}).input("claude-kind", "", "x", false).signalWaiting(context.Background())
		assert.Equal(t, c.kind, kind, "%s: %s", c.st.State, why)
	}

	withState(t, AgentState{}, false)
	_, _, _, ok := (&fakeAgent{}).input("claude-none", "", "x", false).signalWaiting(context.Background())
	assert.False(t, ok, "no signals for the session: the screen rule decides alone")
}

// Shadow mode (phase 1): decisions follow the screen rule, and the signal rule's
// decision is logged next to it once per change.
func TestShadowModeLogsBothDecisions(t *testing.T) {
	fastGate(t)
	t.Setenv("CORAL_GATE_MODE", "")
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(syncWriter{w: &buf, mu: &mu}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	withState(t, AgentState{State: StateInTurn, LastEvent: time.Now(), CLIVersion: "2.1.300"}, true)

	a := &fakeAgent{screen: idleInputBox}
	d, err := DeliverAutomated(context.Background(), a.input("claude-shadow", "k", "nudge", false))
	require.NoError(t, err)
	assert.Equal(t, Sent, d, "shadow: the screen rule still decides")
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	assert.Contains(t, out, "automated input decision")
	assert.Contains(t, out, "agree=false")
	assert.Contains(t, out, `new="hold: signals: in turn"`)
	assert.Equal(t, 1, strings.Count(out, "automated input decision"))
	for _, f := range []string{"agent_type=claude", "state=in_turn", "cli_version=2.1.300", "kind=\"\""} {
		assert.Contains(t, out, f, "the report's fields (#3494)")
	}

	// A send keyed by its own text (a route's board message) logs a hash, never the text.
	c := &fakeAgent{screen: idleInputBox}
	_, err = DeliverAutomated(context.Background(), c.input("claude-shadow-text", "", "a private board message", false))
	require.NoError(t, err)
	mu.Lock()
	out = buf.String()
	mu.Unlock()
	assert.NotContains(t, out, "a private board message")
	assert.Contains(t, out, "key=text:")

	t.Setenv("CORAL_GATE_MODE", "signals")
	b := &fakeAgent{screen: idleInputBox}
	d, err = DeliverAutomated(context.Background(), b.input("claude-signals", "k", "nudge", false))
	require.NoError(t, err)
	assert.Equal(t, Held, d, "signals mode: in turn holds")
	assert.Empty(t, b.sent())
	DropHeld("claude-signals", "test done")
}

// The hand-off retires the matching held sends exactly once: none is typed later, the
// others stay held, and HeldEndedFunc runs for reported ones.
func TestTakeHeldRetiresMatchingSends(t *testing.T) {
	fastGate(t)
	a := &fakeAgent{screen: permissionMenu}
	ctx := context.Background()
	_, _ = DeliverAutomated(ctx, a.input("claude-take", "board-unread:team", "nudge", false))
	_, _ = DeliverAutomated(ctx, a.input("claude-take", "agent-ui", "ui notice", false))
	require.Equal(t, 2, HeldCount("claude-take"))

	taken, ok := TakeHeld(ctx, "claude-take", "handed off", func(k string) bool { return strings.HasPrefix(k, "board-unread:") })
	require.True(t, ok)
	require.Len(t, taken, 1)
	assert.Equal(t, "board-unread:team", taken[0].Key)
	assert.Equal(t, 1, HeldCount("claude-take"), "the UI notice stays held")

	a.show(idleInputBox)
	require.Eventually(t, func() bool { return len(a.sent()) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, []string{"ui notice"}, a.sent(), "the handed-off nudge is never typed")
}

// A send in progress holds the session's lock; TakeHeld waits for it until its ctx is
// done and then takes nothing (the hand-off's short timeout never stalls the agent).
func TestTakeHeldGivesUpWhenASendIsInProgress(t *testing.T) {
	fastGate(t)
	unlock := gate.lockSession("claude-busy-take")
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	taken, ok := TakeHeld(ctx, "claude-busy-take", "handed off", func(string) bool { return true })
	assert.Nil(t, taken)
	assert.False(t, ok, "a send in progress: nothing taken, and the caller knows")
	assert.Less(t, time.Since(start), time.Second)
}

// Windows PTY (P3): idle by signals, but a person typed since the turn ended: their
// draft can't be read, so the send waits.
type ptyAgent struct {
	fakeAgent
	typedAt time.Time
}

func (p *ptyAgent) HumanInputSince(_, _ string, t time.Time) bool { return p.typedAt.After(t) }

func TestPTYHoldsWhenAPersonTypedSinceTheTurnEnded(t *testing.T) {
	fastGate(t)
	stop := time.Now().Add(-time.Minute)
	withState(t, AgentState{State: StateIdle, LastStop: stop}, true)
	p := &ptyAgent{fakeAgent: fakeAgent{screen: idleInputBox}, typedAt: stop.Add(10 * time.Second)}
	in := p.input("claude-pty", "", "nudge", false)
	in.Term = p
	wait, why, _, ok := in.signalWaiting(context.Background())
	require.True(t, ok)
	assert.True(t, wait)
	assert.Equal(t, whyDraft, why)

	p.typedAt = stop.Add(-time.Second)
	wait, _, _, _ = in.signalWaiting(context.Background())
	assert.False(t, wait, "nobody typed since the Stop")
}

// syncWriter serialises writes to a buffer the test reads.
type syncWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (s syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// The PTY backend records a person's keystrokes (the dashboard terminal's input), not
// Coral's own SendInput, for the gate's PTY rule.
func TestPTYBackendRecordsHumanInput(t *testing.T) {
	b := NewPTYBackend()
	before := time.Now().Add(-time.Second)
	_ = b.SendInput(terminalinput.Unclassified, "claude-x", []byte("automated")) // no session: an error, and no record
	assert.False(t, b.HumanInputSince("claude-x", before), "Coral's own input is not a person's")
	_ = b.SendHumanInput("claude-x", []byte("h"))
	assert.True(t, b.HumanInputSince("claude-x", before))
	assert.False(t, b.HumanInputSince("claude-x", time.Now().Add(time.Second)), "nothing typed after t")
	assert.False(t, b.HumanInputSince("claude-y", before), "per session")
	var _ HumanInputReader = (*PTYSessionTerminal)(nil)
	var _ HumanInputSender = b
}

// The signal rule over every real capture in the screen corpus (#3484): phase 2's
// switch is gated on it as well as on the shadow log. For each screen and CLI, the
// state from signals and what the corpus labels say must give the same decision.
func TestSignalRuleOverCorpus(t *testing.T) {
	fastGate(t)
	dirs, err := filepath.Glob(filepath.Join("testdata", "screens", "*", "*"))
	require.NoError(t, err)
	require.NotEmpty(t, dirs)
	old := time.Now().Add(-10 * time.Minute)
	checked := 0
	for _, dir := range dirs {
		cli := filepath.Base(filepath.Dir(dir))
		raw, err := os.ReadFile(filepath.Join(dir, "expect.json"))
		require.NoError(t, err)
		var labels map[string]screenLabel
		require.NoError(t, json.Unmarshal(raw, &labels))
		for file, l := range labels {
			b, err := os.ReadFile(filepath.Join(dir, file))
			require.NoError(t, err)
			plain := sgrRe.ReplaceAllString(string(b), "")
			decide := func(st AgentState) (bool, bool, string) {
				withState(t, st, true)
				a := &fakeAgent{screen: plain}
				in := a.input(cli+"-corpus", "", corpusNudge, false)
				in.AgentType = cli
				wait, why, kind, ok := in.signalWaiting(context.Background())
				require.True(t, ok)
				return wait, kind != "", why
			}
			name := filepath.Join(dir, file) + " (" + l.State + ")"
			wait, prompt, why := decide(AgentState{State: StateIdle})
			assert.Equal(t, l.Prompt, wait, "%s idle: %s", name, why)
			assert.Equal(t, l.PromptHold, prompt, "%s idle: %s", name, why)

			wait, prompt, why = decide(AgentState{State: StateStarting})
			assert.True(t, wait, name)
			assert.Equal(t, l.PromptHold, prompt, "%s starting: %s", name, why)

			wait, prompt, _ = decide(AgentState{State: StateBlocked, Kind: "permission"})
			assert.True(t, wait && prompt, "%s blocked", name)

			wait, prompt, why = decide(AgentState{State: StateInTurn, LastEvent: time.Now()})
			assert.True(t, wait, name)
			if hasPromptSignal(cli) {
				assert.False(t, prompt, "%s in turn: the CLI signals its prompts: %s", name, why)
			}

			if !hasPromptSignal(cli) {
				_, prompt, why = decide(AgentState{State: StateInTurn, LastEvent: time.Now(), ToolPending: true})
				assert.Equal(t, l.PromptHold, prompt, "%s tool pending: %s", name, why)
			}

			// Claude's stale in-turn relaxation: only an empty box with no prompt
			// and no working line.
			wait, _, why = decide(AgentState{State: StateInTurn, LastEvent: old})
			relax := cli == "claude" && l.Box == "empty" && !l.Prompt && l.State != "working"
			assert.Equal(t, !relax, wait, "%s stale in turn: %s", name, why)
			checked++
		}
	}
	t.Logf("%d captures checked", checked)
}

// #3662 P6: each decision line says how long the signals had been in their state.
func TestShadowLineCarriesTheStatesAge(t *testing.T) {
	fastGate(t)
	t.Setenv("CORAL_GATE_MODE", "")
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(syncWriter{w: &buf, mu: &mu}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	withState(t, AgentState{State: StateInTurn, Since: time.Now().Add(-12 * time.Minute), LastEvent: time.Now(), CLIVersion: "2.1.300"}, true)
	a := &fakeAgent{screen: idleInputBox}
	_, err := DeliverAutomated(context.Background(), a.input("claude-state-age", "k", "nudge", false))
	require.NoError(t, err)
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	assert.Contains(t, out, "state_for=12m0s")
}
