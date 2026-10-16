package tmux

import (
	"context"
	"os/exec"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3439 (finding C): a capture of a session resolves its pane once; later captures are one tmux
// exec (capture-pane), not two (list-panes first). A gone pane drops the cached target and is
// looked up again, so it never reaches another pane.
func TestCapturePaneCachesTheTarget(t *testing.T) {
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	var mu sync.Mutex
	var cmds []string
	runObserver = func(args []string) { mu.Lock(); cmds = append(cmds, args[0]); mu.Unlock() }
	t.Cleanup(func() { runObserver = nil })
	reset := func() []string { mu.Lock(); defer mu.Unlock(); out := cmds; cmds = nil; return out }

	c.forgetPaneStates() // the setup's list is the shared snapshot (#4049): start without one
	_, err := c.CapturePane(ctx, sibling, 50, "claude", siblingSID)
	require.NoError(t, err)
	first := reset()
	assert.Contains(t, first, "list-panes", "the first capture resolves the pane")
	assert.Contains(t, first, "capture-pane")

	text, err := c.CapturePane(ctx, sibling, 50, "claude", siblingSID)
	require.NoError(t, err)
	assert.Equal(t, []string{"capture-pane"}, reset(), "later captures are one exec")
	assert.Equal(t, text, siblingScreen(t, c))
	reset()

	// The session goes: the cached target fails, is dropped, and the lookup finds nothing.
	require.NoError(t, exec.Command("tmux", "-S", c.SocketPath, "kill-session", "-t", "="+sibling).Run())
	gone, err := c.CapturePane(ctx, sibling, 50, "claude", siblingSID)
	require.NoError(t, err)
	assert.Empty(t, gone, "a gone pane captures nothing")
	after := reset()
	assert.Contains(t, after, "list-panes", "looked up again after the failed capture")
	_, cached := c.captureTarget(sibling + "\x00claude\x00" + siblingSID)
	assert.False(t, cached, "the dead target is not kept")

	// Without a session id nothing is cached (a name can move to another agent).
	_, _ = c.CapturePane(ctx, "team", 50, "", "")
	_, cachedName := c.captureTarget("team\x00\x00")
	assert.False(t, cachedName)
}

// Killing a session forgets its cached capture target (nothing grows over a long run).
func TestKillSessionForgetsTheCaptureTarget(t *testing.T) {
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	_, err := c.CapturePane(ctx, sibling, 50, "claude", siblingSID)
	require.NoError(t, err)
	_, cached := c.captureTarget(sibling + "\x00claude\x00" + siblingSID)
	require.True(t, cached)
	require.NoError(t, c.KillSessionOnly(ctx, sibling, "claude", siblingSID))
	_, cached = c.captureTarget(sibling + "\x00claude\x00" + siblingSID)
	assert.False(t, cached, "forgotten on kill")
}
