package vtscreen

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func numbered(n int) []byte {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "a%03d\r\n", i)
	}
	return []byte(b.String())
}

// RenderScrollback is the screen with the rows that scrolled off above it, as tmux's capture-pane
// -S -<n> gives a pane (#27): the last n of them, oldest first, then the screen's rows.
func TestRenderScrollbackKeepsTheRowsScrolledOff(t *testing.T) {
	out := numbered(10) // a001..a010, then the cursor on an empty last row
	assert.Equal(t, "a009\na010\n", Render(out, 20, 3))
	assert.Equal(t, "a005\na006\na007\na008\na009\na010\n", RenderScrollback(out, 20, 3, 4))
	assert.Equal(t, strings.TrimSuffix(string(numbered(10)), "\r\n")+"\r\n",
		strings.ReplaceAll(RenderScrollback(out, 20, 3, 100), "\n", "\r\n"), "all of it while it fits")
}

func TestRenderScrollbackWithoutHistoryIsRender(t *testing.T) {
	out := append(numbered(50), []byte("\x1b[2J\x1b[H\x1b[31mframe\x1b[0m\x1b[5;3Hbox")...)
	assert.Equal(t, Render(out, 40, 10), RenderScrollback(out, 40, 10, 0))
	assert.Equal(t, Render(out, 40, 10), RenderScrollback(out, 40, 10, -5), "a negative history is none")
}

// The history is capped at MaxHistory rows, and keeping it costs O(1) a scroll: an agent's output is
// hostile input, replayed on every read.
func TestRenderScrollbackIsCapped(t *testing.T) {
	out := numbered(5 * MaxHistory)
	got := strings.Split(RenderScrollback(out, 20, 5, 10*MaxHistory), "\n")
	assert.Len(t, got, MaxHistory+5)
	assert.Equal(t, fmt.Sprintf("a%03d", 5*MaxHistory-MaxHistory-3), got[0], "the newest MaxHistory rows above the screen")

	flood := []byte(strings.Repeat("\n", MaxInput)) // half a megabyte of line feeds
	assert.Len(t, strings.Split(RenderScrollback(flood, 80, 24, MaxHistory), "\n"), MaxHistory+24)
}

// An application on the alternate screen (Codex, agy) scrolls there without leaving rows in the scrollback, as in tmux; the main
// screen's own history and the screen it comes back to are what capture shows.
func TestAlternateScreenLeavesNoScrollback(t *testing.T) {
	out := append(numbered(6), []byte("\x1b[?1049h")...)
	out = append(out, strings.ReplaceAll(string(numbered(8)), "a0", "alt")...)
	out = append(out, []byte("\x1b[?1049l")...)
	got := RenderScrollback(out, 20, 3, 100)
	assert.NotContains(t, got, "alt", "nothing from the alternate screen")
	assert.Contains(t, got, "a001\n", "the main screen's history is kept")
	assert.Contains(t, got, "a006\n", "and the screen it comes back to")
}
