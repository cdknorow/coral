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
)

// #3655 F4: a held send is re-checked every couple of seconds, so state_disagreement (signals
// idle, a prompt on screen) is logged once per window, not per check, and two separate windows
// are still two lines (QA's point 5).
func TestStateDisagreementIsLoggedOncePerWindow(t *testing.T) {
	fastGate(t)
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(syncWriter{w: &buf, mu: &mu}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	t.Cleanup(func() { noteDisagreement("claude-window", "", time.Now()) })
	withState(t, AgentState{State: StateIdle, LastEvent: time.Now(), CLIVersion: "2.1.300"}, true)
	lines := func() int {
		mu.Lock()
		defer mu.Unlock()
		return strings.Count(buf.String(), "state_disagreement")
	}
	menu := " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n"
	a := &fakeAgent{screen: menu}
	check := func() {
		t.Helper()
		waiting, _, _, ok := a.input("claude-window", "k", "nudge", false).signalWaiting(context.Background())
		assert.True(t, ok)
		assert.True(t, waiting, "the screen still holds the send")
	}

	for i := 0; i < 10; i++ {
		check()
	}
	assert.Equal(t, 1, lines(), "ten re-checks of one window: one line")

	a.screen = idleInputBox // the menu was answered: a decision without a disagreement
	a.input("claude-window", "k", "nudge", false).signalWaiting(context.Background())
	a.screen = menu // a second menu later on: a new window
	check()
	check()
	assert.Equal(t, 2, lines(), "a second window: a second line")

	// A window whose held send was dropped (no decision ends it) and a menu much later: new line.
	assert.True(t, noteDisagreement("claude-gap", "selection menu", time.Now()), "a window opens")
	assert.False(t, noteDisagreement("claude-gap", "selection menu", time.Now()))
	assert.True(t, noteDisagreement("claude-gap", "selection menu", time.Now().Add(2*disagreementGap)), "after the gap: a new window")
	assert.True(t, noteDisagreement("claude-gap", "prompt question: Allow?", time.Now().Add(2*disagreementGap)), "a new reason: a new line")
	noteDisagreement("claude-gap", "", time.Now())
}
