package ptymanager

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Codex-style frame (rows placed by cursor moves); internal/vtscreen's tests cover the rendering itself.
const codexStyleMenu = "\x1b[1;1H\x1b[J\x1b[2;3HHooks need review" +
	"\x1b[4;1H› 1. Review hooks\x1b[5;3H2.\x1b[5;6HTrust\x1b[5;12Hall\x1b[5;16Hand\x1b[5;20Hcontinue" +
	"\x1b[4;1H  1. Review hooks\x1b[5;1H› 2. Trust all and continue"

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
