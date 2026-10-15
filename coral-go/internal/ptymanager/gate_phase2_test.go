package ptymanager

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// phase2 turns the Operator's gate_signals_claude setting on for a test.
func phase2(t *testing.T, on bool) {
	t.Helper()
	t.Setenv("CORAL_GATE_MODE", "")
	SetSignalsSettingFunc(func() bool { return on })
	t.Cleanup(func() { SetSignalsSettingFunc(nil) })
}

// Gate phase 2 for Claude (#3578, the Operator's decision on #3494): mid-turn a routine nudge
// waits for the turn to end and then goes once; a mention goes at once (as with the screen
// rule); a menu still holds even a mention.
func TestPhase2HoldsRoutineSendsForTheTurn(t *testing.T) {
	fastGate(t)
	phase2(t, true)
	ctx := context.Background()
	st := withState(t, AgentState{State: StateInTurn, Since: time.Now(), LastEvent: time.Now()}, true)
	_ = st

	a := &fakeAgent{screen: idleInputBox}
	d, err := DeliverAutomated(ctx, a.input("claude-p2-routine", "board-unread:team", "You have 1 unread message", false))
	require.NoError(t, err)
	assert.Equal(t, Held, d, "a routine nudge waits for the turn")
	assert.Empty(t, a.sent())
	assert.Equal(t, 1, HeldCount("claude-p2-routine"))

	m := &fakeAgent{screen: idleInputBox}
	in := m.input("claude-p2-mention", "board-unread:team", "You have 1 unread message", false)
	in.Mention = true
	d, err = DeliverAutomated(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, Sent, d, "a mention goes mid-turn")
	assert.Equal(t, []string{"You have 1 unread message"}, m.sent())

	menu := &fakeAgent{screen: permissionMenu}
	in = menu.input("claude-p2-menu", "board-unread:team", "a mention", false)
	in.Mention = true
	d, err = DeliverAutomated(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, Held, d, "a menu holds even a mention")
	assert.Empty(t, menu.sent())
	DropHeld("claude-p2-menu", "test done")

	// The turn ends: the held nudge goes, once.
	*st = AgentState{State: StateIdle, Since: time.Now(), LastEvent: time.Now(), LastStop: time.Now()}
	require.Eventually(t, func() bool { return len(a.sent()) == 1 }, 3*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // sleep-ok: a second loop pass must not send it again
	assert.Equal(t, []string{"You have 1 unread message"}, a.sent())
	assert.Equal(t, 0, HeldCount("claude-p2-routine"))
}

// After inTurnFallback in one turn, a routine send follows the screen rule (nothing waits on
// a turn for ever), and the decision line says so.
func TestPhase2LongTurnFallsBackToTheScreen(t *testing.T) {
	fastGate(t)
	phase2(t, true)
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(syncWriter{w: &buf, mu: &mu}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	withState(t, AgentState{State: StateInTurn, Since: time.Now().Add(-inTurnFallback - time.Minute), LastEvent: time.Now()}, true)

	a := &fakeAgent{screen: idleInputBox}
	d, err := DeliverAutomated(context.Background(), a.input("claude-p2-long", "k", "nudge", false))
	require.NoError(t, err)
	assert.Equal(t, Sent, d, "a turn over the limit no longer holds a routine send")
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	assert.Contains(t, out, `decided="screen: in turn over 30m0s"`)
	assert.Contains(t, out, "mode=signals")
}

// The setting off (the default), the kill switch, and other CLIs: the screen rule decides,
// so a routine nudge mid-turn goes as before.
func TestPhase2OnlyForClaudeAndOnlyWhenOn(t *testing.T) {
	fastGate(t)
	ctx := context.Background()
	withState(t, AgentState{State: StateInTurn, Since: time.Now(), LastEvent: time.Now()}, true)
	for _, c := range []struct {
		name, mode string
		on         bool
	}{
		{"claude-p2-off", "", false},
		{"claude-p2-kill", "screen", true},
		{"codex-p2-on", "", true},
		{"gemini-p2-on", "", true},
	} {
		SetSignalsSettingFunc(func() bool { return c.on })
		t.Setenv("CORAL_GATE_MODE", c.mode)
		a := &fakeAgent{screen: idleInputBox}
		d, err := DeliverAutomated(ctx, a.input(c.name, "k", "nudge", false))
		require.NoError(t, err, c.name)
		assert.Equal(t, Sent, d, "%s: the screen rule decides", c.name)
	}
	SetSignalsSettingFunc(nil)
	assert.True(t, func() bool { t.Setenv("CORAL_GATE_MODE", "signals"); return gateFollowsSignals("codex") }(), "=signals still forces it for every CLI (trials)")
}

// Held sends are capped in size too; the oldest goes, never the newest, and the log names the
// sender, never the text.
func TestHeldSendsAreCappedInSize(t *testing.T) {
	fastGate(t)
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(syncWriter{w: &buf, mu: &mu}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	a := &fakeAgent{screen: permissionMenu}
	name := "claude-p2-size"
	big := strings.Repeat("x", 100<<10)
	for i, k := range []string{"a", "b", "c"} {
		d, err := DeliverAutomated(context.Background(), a.input(name, k, big+string(rune('0'+i)), false))
		require.NoError(t, err)
		assert.Equal(t, Held, d)
	}
	assert.Equal(t, 2, HeldCount(name), "three 100 KiB sends don't fit in 256 KiB: the oldest is dropped")
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	assert.Contains(t, out, "over the size limit")
	assert.NotContains(t, out, big[:64])
	DropHeld(name, "test done")
}
