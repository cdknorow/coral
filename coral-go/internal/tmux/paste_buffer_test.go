package tmux

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// #4381 (Security's #4371 SHOULD 2): text Coral pastes into a pane (a long message, a multi-line
// one) leaves nothing in tmux's buffer stack, which a tmux server that outlives Coral keeps.
func TestPastedTextLeavesNoTmuxBuffer(t *testing.T) {
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	target := sibling + ":0.0"
	long := strings.Repeat("a long dashboard message, ", 200) // ~5 KB on one line
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, target, long))
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, target, "line one\nline two"))
	out, err := c.run(ctx, "list-buffers")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out), "no buffer is left after the pastes")
}

// A paste that fails (the pane is gone) deletes its buffer too.
func TestAFailedPasteLeavesNoTmuxBuffer(t *testing.T) {
	c, _ := newSiblingOnly(t)
	ctx := context.Background()
	err := c.pasteToTarget(ctx, terminalinput.Unclassified, "claude-no-such-session:0.0", "line one\nline two")
	require.Error(t, err)
	out, err := c.run(ctx, "list-buffers")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out))
}
