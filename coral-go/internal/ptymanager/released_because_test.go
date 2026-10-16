package ptymanager

import (
	"bytes"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
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

var draftReleasedRuns atomic.Int64

func TestADraftHoldIsLoggedAsReleasedByTheDraft(t *testing.T) {
	fastGate(t)
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(syncWriter{w: &buf, mu: &mu}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A name of its own each run: under -count the last run's queue for the same name was still
	// being flushed, and took this hold before it could be counted (#3788).
	name := fmt.Sprintf("claude-draft-released-%d", draftReleasedRuns.Add(1))
	t.Cleanup(func() { DropHeld(name, "test done") })
	a := &fakeAgent{screen: idleInputBox}
	gate.holdPaste(a.input(name, "board-unread:bb", "nudge", false), whyDraft, false, "")
	require.Equal(t, 1, HeldCount(name))
	// The queue drops the item before typing it, so an empty queue alone can be seen before the
	// send (#3788); wait for the send.
	require.Eventually(t, func() bool { return HeldCount(name) == 0 && len(a.sent()) > 0 }, 5*time.Second, 20*time.Millisecond, "typed once the box is free")
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
