package tmux

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3847: an agent's pane pid and size come from one shared list-panes (kept for a second), not
// a display-message per agent per check; they are what the pane itself says.
func TestPaneStateIsSharedAndRight(t *testing.T) {
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	_, err := c.CapturePane(ctx, sibling, 50, "claude", siblingSID) // resolves the pane target
	require.NoError(t, err)
	wantPID, err := c.DisplayForAgent(ctx, sibling, "claude", siblingSID, "#{pane_pid}")
	require.NoError(t, err)
	wantSize, err := c.DisplayForAgent(ctx, sibling, "claude", siblingSID, "#{pane_width} #{pane_height}")
	require.NoError(t, err)

	var mu sync.Mutex
	var cmds []string
	runObserver = func(args []string) { mu.Lock(); cmds = append(cmds, args[0]); mu.Unlock() }
	t.Cleanup(func() { runObserver = nil })

	for i := 0; i < 5; i++ {
		st, ok := c.agentPaneState(ctx, sibling, "claude", siblingSID)
		require.True(t, ok)
		assert.Equal(t, strings.TrimSpace(wantPID), strconv.Itoa(st.pid))
		cols, rows, err := c.PaneSizeForAgent(ctx, sibling, "claude", siblingSID)
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSpace(wantSize), strconv.Itoa(cols)+" "+strconv.Itoa(rows))
		// Its answer comes from the process table (cached for a second across tests); what is
		// checked here is the tmux it runs.
		_, _, _ = c.AgentShellIdle(ctx, sibling, "claude", siblingSID)
	}
	mu.Lock()
	defer mu.Unlock()
	lists := 0
	for _, c := range cmds {
		assert.NotEqual(t, "display-message", c, "no per-agent display-message")
		if c == "list-panes" {
			lists++
		}
	}
	// At most one: since #4049 the list the setup's pane lookup took is the same shared snapshot,
	// so within its second there may be none at all.
	assert.LessOrEqual(t, lists, 1, "one list for all of them within a second: %v", cmds)
}

// v1.4.63 (#3847's follow-up): Coral's own resize reads back at once. The shared list kept the
// old size for up to a second, so the dashboard's fit and pane-size checks saw the size from
// before their resize.
func TestPaneSizeIsFreshAfterOwnResize(t *testing.T) {
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	_, err := c.CapturePane(ctx, sibling, 50, "claude", siblingSID) // resolves the pane target
	require.NoError(t, err)
	cols, _, err := c.PaneSizeForAgent(ctx, sibling, "claude", siblingSID) // fills the shared list
	require.NoError(t, err)
	for _, want := range []int{cols + 7, cols + 3} {
		require.NoError(t, c.ResizePane(ctx, sibling, want, "claude", siblingSID))
		got, _, err := c.PaneSizeForAgent(ctx, sibling, "claude", siblingSID)
		require.NoError(t, err)
		assert.Equal(t, want, got, "the size right after Coral's own resize")
	}
	target, ok := c.captureTarget(sibling + "\x00claude\x00" + siblingSID)
	require.True(t, ok)
	require.NoError(t, c.ResizePaneTarget(ctx, target, cols+5, 0))
	got, _, err := c.PaneSizeForAgent(ctx, sibling, "claude", siblingSID)
	require.NoError(t, err)
	assert.Equal(t, cols+5, got, "and after a resize by target")
}

// A list taken while a pane changed is not kept: the next caller asks again.
func TestPaneListTakenDuringAChangeIsNotKept(t *testing.T) {
	c := &Client{}
	c.paneStatesMu.Lock()
	gen := c.paneStatesGen
	c.paneStatesMu.Unlock()
	c.forgetPaneStates() // a resize finishes while the list runs
	c.paneStatesMu.Lock()
	defer c.paneStatesMu.Unlock()
	assert.NotEqual(t, gen, c.paneStatesGen)
	assert.Nil(t, c.snap)
}

// #4049: the agent discovery's ListPanes and the pane pid and size questions read ONE shared
// list-panes per paneStateTTL, and it lists what tmux lists.
func TestListPanesSharesOneSnapshot(t *testing.T) {
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	_, err := c.CapturePane(ctx, sibling, 50, "claude", siblingSID) // resolves the pane target
	require.NoError(t, err)
	direct := c.listPanesOnSocket(ctx, c.SocketPath)
	c.forgetPaneStates()

	var mu sync.Mutex
	var cmds []string
	runObserver = func(args []string) { mu.Lock(); cmds = append(cmds, args[0]); mu.Unlock() }
	t.Cleanup(func() { runObserver = nil })
	for i := 0; i < 5; i++ {
		panes, err := c.ListPanes(ctx)
		require.NoError(t, err)
		assert.Equal(t, direct, panes, "the same panes as a list of its own")
		_, ok := c.agentPaneState(ctx, sibling, "claude", siblingSID)
		require.True(t, ok)
	}
	mu.Lock()
	defer mu.Unlock()
	lists := 0
	for _, c := range cmds {
		if c == "list-panes" {
			lists++
		}
	}
	assert.Equal(t, 1, lists, "one list for the discovery and the pane states: %v", cmds)
}

// The snapshot is fresh: a pane Coral creates or ends shows at once, and an old snapshot isn't
// used past paneStateTTL.
func TestListPanesSnapshotStaysFresh(t *testing.T) {
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	has := func(name string) bool {
		panes, err := c.ListPanes(ctx)
		require.NoError(t, err)
		for _, p := range panes {
			if p.SessionName == name {
				return true
			}
		}
		return false
	}
	require.True(t, has(sibling))
	extra := "claude-" + strings.Repeat("e", 8) + "-0000-4000-8000-000000000000"
	_, err := c.run(ctx, "new-session", "-d", "-s", extra, "sleep 30")
	require.NoError(t, err)
	assert.True(t, has(extra), "a session Coral created shows at once")
	_, err = c.run(ctx, "kill-session", "-t", extra)
	require.NoError(t, err)
	assert.False(t, has(extra), "and one it ended is gone at once")

	c.paneStatesMu.Lock()
	first := c.snap
	c.paneStatesMu.Unlock()
	require.NotNil(t, first)
	time.Sleep(paneStateTTL + 100*time.Millisecond)
	has(sibling)
	c.paneStatesMu.Lock()
	defer c.paneStatesMu.Unlock()
	assert.NotSame(t, first, c.snap, "past paneStateTTL the list is taken again")
}
