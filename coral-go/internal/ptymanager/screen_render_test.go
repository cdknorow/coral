package ptymanager

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Codex draws a menu as ratatui does (#4543): every row, and every word of a row, placed with a cursor move,
// no newline anywhere, inside a synchronized update; a later frame redraws only the rows that changed.
const codexStyleMenu = "\x1b[?2026h\x1b[1;1H\x1b[J" +
	"\x1b[2;3H\x1b[1mHooks need review" +
	"\x1b[4;1H\x1b[22m\x1b[38;5;6;49m› 1. Review hooks" +
	"\x1b[5;3H\x1b[39;49m2.\x1b[5;6HTrust\x1b[5;12Hall\x1b[5;16Hand\x1b[5;20Hcontinue" +
	"\x1b[7;3H\x1b[2mPress enter to confirm or esc to go back\x1b[0m\x1b[?2026l" +
	"\x1b[?2026h\x1b[4;1H  1. Review hooks\x1b[5;1H\x1b[1m› 2. Trust all and continue\x1b[0m\x1b[?2026l"

func TestRenderScreenDrawsCursorPlacedRows(t *testing.T) {
	got := strings.Split(RenderScreen([]byte(codexStyleMenu), 60, 8), "\n")
	require.Len(t, got, 8, "one line per row")
	assert.Equal(t, "  Hooks need review", got[1])
	assert.Equal(t, "  1. Review hooks", got[3], "the later frame's redraw replaced the row")
	assert.Equal(t, "› 2. Trust all and continue", got[4], "words placed one by one read as one line")
	assert.Equal(t, "  Press enter to confirm or esc to go back", got[6])
	assert.Equal(t, "", got[7], "trailing blanks trimmed")
	// In the raw text of the first frame, what the prompt reader got before, the label is not there.
	first := codexStyleMenu[:strings.Index(codexStyleMenu, "\x1b[?2026l")]
	assert.Contains(t, RenderScreen([]byte(first), 60, 8), "2. Trust all and continue")
	assert.NotContains(t, first, "Trust all and continue")
}

// What Claude Code sends at launch around its first screen: a kitty keyboard query vt10x would take for
// "restore the cursor", a graphics query whose payload it would print, a mode report request. None draws.
func TestRenderScreenIgnoresTerminalQueries(t *testing.T) {
	out := "banner line 1\r\nbanner line 2\r\n" +
		"\x1b[>0q\x1b[?u\x1b]7501;?\x07\x1b[c\x1b[?2026$p\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\\x1b[16t\x1b[0 q" +
		"\x1b[?25l\x1b[?2004hDo you want to use this API key?"
	got := strings.Split(RenderScreen([]byte(out), 60, 4), "\n")
	assert.Equal(t, []string{"banner line 1", "banner line 2", "Do you want to use this API key?", ""}, got)
	assert.Equal(t, "\x1b[?25l\x1b[?2004h", string(dropTerminalQueries([]byte("\x1b[?25l\x1b[?u\x1b[?2004h\x1b[>7u"))), "mode sets stay")
}

func TestRenderScreenWithoutASize(t *testing.T) {
	assert.Len(t, strings.Split(RenderScreen([]byte("hi"), 0, 0), "\n"), defaultRenderRows)
}

// The PTY terminal renders the session's replay at the size its PTY was last given.
func TestPTYRenderedScreenUsesTheSessionsSize(t *testing.T) {
	b, s, _ := newFakeBackend(t, "codex-hooks")
	s.ringMax = 1 << 16
	s.ring = []byte(codexStyleMenu)
	s.cols, s.rows = 60, 8
	term := NewPTYSessionTerminal(b)
	var _ RenderedScreenReader = term
	screen, err := term.RenderedScreen(context.Background(), "codex-hooks", "codex", "")
	require.NoError(t, err)
	lines := strings.Split(screen, "\n")
	assert.Len(t, lines, 8)
	assert.Equal(t, "› 2. Trust all and continue", lines[4])
}
