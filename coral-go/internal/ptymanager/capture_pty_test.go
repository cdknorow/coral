package ptymanager

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A TUI's output as the replay ring holds it: coloured lines, a clear screen, then a frame drawn
// with cursor moves and redrawn several times.
func tuiOutput() []byte {
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "\x1b[32mline %03d\x1b[0m of the transcript\r\n", i)
	}
	for frame := 1; frame <= 50; frame++ {
		fmt.Fprintf(&b, "\x1b[?25l\x1b[2J\x1b[H\x1b[1mWorking (%ds)\x1b[0m\x1b[22;1H\x1b[2K> draft text\x1b[24;1H\x1b[2mfooter\x1b[0m\x1b[?25h", frame)
	}
	return []byte(b.String())
}

// #27 (audit F9): the PTY's capture is the screen as plain text with up to `lines` rows of what
// scrolled off above it (tmux's capture-pane -p -S -<lines>), never the raw ring: coral-board peek
// --lines 30 returned 107 KB of escape sequences and old frames.
func TestPTYCaptureOutputIsTheRenderedScreenNotTheRawRing(t *testing.T) {
	b, s, _ := newFakeBackend(t, "capture-agent")
	raw := tuiOutput()
	s.ring, s.ringMax = raw, len(raw)+1
	s.cols, s.rows = 80, 24
	term := NewPTYSessionTerminal(b)

	out, err := term.CaptureOutput(context.Background(), "capture-agent", 30, "", "")
	require.NoError(t, err)
	assert.NotContains(t, out, "\x1b", "no escape sequences")
	lines := strings.Split(out, "\n")
	assert.LessOrEqual(t, len(lines), 30+24, "at most lines of scrollback above the 24-row screen")
	assert.Contains(t, out, "Working (50s)", "the last frame")
	assert.NotContains(t, out, "Working (49s)", "never an older frame")
	assert.Contains(t, out, "> draft text")
	assert.Contains(t, out, "footer")
	// Lines 278-300 were on the screen the first frame cleared: never scrolled off, so not kept (no
	// scroll-on-clear, see vtscreen.RenderScrollback); 277 is the last row that scrolled off.
	assert.Contains(t, out, "line 277 of the transcript", "the transcript's end that scrolled off above the frame")
	assert.NotContains(t, out, "line 001", "only the last lines of scrollback")
	assert.Less(t, len(out), len(raw)/10, "the size of what tmux gives, not of the ring (%d bytes of %d)", len(out), len(raw))

	screenOnly, err := term.CaptureOutput(context.Background(), "capture-agent", 0, "", "")
	require.NoError(t, err)
	assert.Len(t, strings.Split(screenOnly, "\n"), 24, "lines=0: the screen alone")
}

func TestPTYCaptureOutputOfAGoneSessionIsEmpty(t *testing.T) {
	b, _, _ := newFakeBackend(t, "capture-agent")
	out, err := NewPTYSessionTerminal(b).CaptureOutput(context.Background(), "claude-00000000-0000-0000-0000-000000000000", 30, "claude", "00000000-0000-0000-0000-000000000000")
	require.NoError(t, err)
	assert.Empty(t, out, "never another session's output")
}
