//go:build !windows

package ptymanager

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6481 (#6466's root cause): a Codex repaints only the cells that change. Its composer ("› Ask Codex to do anything") is drawn once and is not drawn again while the working line above it
// repaints every second, so after a few minutes of work the 256 KiB replay ring holds the working line's repaints and no longer the composer. The screen rendered from that ring has no input box,
// whatever the real screen shows, and the gate held every automated send for it. The screen is the terminal's own state, kept as the output arrives, not a replay of the ring's tail.

// codexLikeStandIn paints a composer and a footer once at the bottom of a 120x40 screen, then repaints one status row over and over (more than the ring holds), then settles.
const codexLikeStandIn = `printf '\033[2J\033[38;1H› Ask Codex to do anything\033[40;1H  ? for shortcuts'; ` +
	`i=0; while [ $i -lt 7000 ]; do printf '\033[30;1H\033[2K• Working (%ds • esc to interrupt) esc to interrupt esc to interrupt' $i; i=$((i+1)); done; ` +
	`printf '\033[30;1H\033[2K• Worked for 9s'; printf '\nREPAINTS-DONE\n'; sleep 30`

func TestTheScreenKeepsTheInputBoxAfterTheReplayRingWrapped(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()
	require.NoError(t, backend.Spawn("ring-wrap", "codex", t.TempDir(), "sid-ring-wrap", codexLikeStandIn, 120, 40))
	ch, err := backend.Attach("ring-wrap", "ws-ring-wrap")
	require.NoError(t, err)
	var seen strings.Builder
	deadline := time.After(2 * time.Minute) // only the ceiling of a stand-in that never ends: the wait itself is for REPAINTS-DONE (#7163)
	for !strings.Contains(seen.String(), "REPAINTS-DONE") {
		select {
		case data, ok := <-ch:
			require.True(t, ok, "the terminal closed")
			seen.Write(data)
			if seen.Len() > 4096 {
				s := seen.String()
				seen.Reset()
				seen.WriteString(s[len(s)-2048:])
			}
		case <-deadline:
			t.Fatal("the stand-in did not finish")
		}
	}
	replay, err := backend.Replay("ring-wrap")
	require.NoError(t, err)
	require.LessOrEqual(t, len(replay), 256*1024+32*1024, "the ring is bounded")
	require.NotContains(t, string(replay), "Ask Codex to do anything", "the composer is no longer in the ring: it was drawn once, long ago")

	screen, err := backend.RenderedScreenText("ring-wrap")
	require.NoError(t, err)
	assert.True(t, InputBoxOnScreen(screen), "the input box is on the screen the terminal holds:\n%s", screen)
	assert.Contains(t, screen, "Ask Codex to do anything")
	assert.Contains(t, screen, "Worked for 9s", "and the working line is the latest one")
}
