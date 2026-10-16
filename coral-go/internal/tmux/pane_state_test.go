package tmux

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

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
	assert.Equal(t, 1, lists, "one list for all of them within a second: %v", cmds)
}
