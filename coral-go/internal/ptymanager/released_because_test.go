package ptymanager

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3520 (QA's bug bash F3): a send held by a person's draft was logged as "delivered
// after the prompt closed" when the draft was cleared. The delivery line names what
// ended the hold.
func TestReleasedBecauseNamesWhatEndedTheHold(t *testing.T) {
	for why, want := range map[string]string{
		whyDraft: "the draft cleared",
		"the agent's input box isn't on screen yet": "the input box was ready",
		"the paste didn't reach the input box":      "the input box was ready",
		"earlier sends still held":                  "the earlier sends were typed",
		"signals: starting":                         "the agent started",
		"signals: in turn":                          "the turn ended",
		busyWhy + "esc to interrupt":                "the agent stopped working",
		"selection menu":                            "the prompt closed",
		"signals: blocked (permission)":             "the prompt closed",
	} {
		assert.Equal(t, want, releasedBecause(why), why)
	}
}

func TestADraftHoldIsLoggedAsReleasedByTheDraft(t *testing.T) {
	fastGate(t)
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(syncWriter{w: &buf, mu: &mu}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	name := "claude-draft-released"
	a := &fakeAgent{screen: idleInputBox}
	gate.holdPaste(a.input(name, "board-unread:bb", "nudge", false), whyDraft, false, "")
	require.Equal(t, 1, HeldCount(name))
	require.Eventually(t, func() bool { return HeldCount(name) == 0 }, 5*time.Second, 20*time.Millisecond, "typed once the box is free")
	assert.Equal(t, []string{"nudge"}, a.sent())

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return bytes.Contains(buf.Bytes(), []byte("automated input delivered after the hold ended"))
	}, time.Second, 10*time.Millisecond)
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	assert.Contains(t, out, `released="the draft cleared"`)
	assert.NotContains(t, out, "prompt closed")
	DropHeld(name, "test done")
}
