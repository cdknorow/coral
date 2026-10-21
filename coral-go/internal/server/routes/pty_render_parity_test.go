package routes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/ptymanager"
)

// The PTY backend's raw output, rendered (internal/vtscreen), shows the same rows tmux captured for the same
// screen (#4543): the end of each screen, row for row, up to the project folder's name (the PTY and tmux
// captures were taken in two folders).
func TestPTYRenderMatchesTheTmuxCapture(t *testing.T) {
	lastRows := func(screen string, n int) []string {
		var rows []string
		for _, l := range strings.Split(screen, "\n") {
			l = strings.TrimRight(strings.ReplaceAll(l, "project2", "project"), " ")
			if strings.TrimSpace(l) != "" {
				rows = append(rows, l)
			}
		}
		return rows[max(0, len(rows)-n):]
	}
	for _, c := range []struct {
		pty, tmux string
		rows      int
		read      func(*testing.T, string) string
	}{
		{"pty_open.raw", "tmux_open.txt", 7, codexHookTrust},
		{"pty_selected_2.raw", "tmux_selected_2.txt", 7, codexHookTrust},
		{"pty_trust.raw", "trust.txt", 10, claudeUnnumbered},
		{"pty_trust_yes.raw", "trust_yes.txt", 10, claudeUnnumbered},
		{"pty_apikey.raw", "apikey.txt", 7, claudeUnnumbered},
		{"pty_apikey_yes.raw", "apikey_yes.txt", 7, claudeUnnumbered},
		{"pty_ready.raw", "ready.txt", 2, claudeUnnumbered}, // the input box above shows a random tip each run
	} {
		rendered := ptymanager.RenderScreen([]byte(c.read(t, c.pty)), 120, 40)
		assert.Equal(t, lastRows(c.read(t, c.tmux), c.rows), lastRows(rendered, c.rows), "%s against %s", c.pty, c.tmux)
	}
}
