package tmux

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listPanesRuns counts the list-panes Coral runs from now on.
func listPanesRuns(t *testing.T) func() int {
	t.Helper()
	var mu sync.Mutex
	n := 0
	runObserver = func(args []string) {
		if args[0] == "list-panes" {
			mu.Lock()
			n++
			mu.Unlock()
		}
	}
	t.Cleanup(func() { runObserver = nil })
	return func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// ageSnapshot makes the shared pane list look d old.
func ageSnapshot(t *testing.T, c *Client, d time.Duration) {
	t.Helper()
	c.paneStatesMu.Lock()
	defer c.paneStatesMu.Unlock()
	require.NotNil(t, c.snap)
	c.snap.at = time.Now().Add(-d)
}

func hasPane(t *testing.T, c *Client, ctx context.Context, name string) bool {
	t.Helper()
	panes, err := c.ListPanes(ctx)
	require.NoError(t, err)
	for _, p := range panes {
		if p.SessionName == name {
			return true
		}
	}
	return false
}

// #4346: callers that only show agents (WithPaneListMaxAge) answer from a pane list up to
// DisplayPaneListAge old, so many of them in that window run one list-panes; a caller without it
// still gets one no older than paneStateTTL, and that fresh list serves the others too.
func TestDisplayCallersShareAnOlderPaneList(t *testing.T) {
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	display := WithPaneListMaxAge(ctx, DisplayPaneListAge)
	require.True(t, hasPane(t, c, ctx, sibling))
	runs := listPanesRuns(t)
	ageSnapshot(t, c, 3*time.Second) // past paneStateTTL, within DisplayPaneListAge

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.True(t, hasPane(t, c, display, sibling))
			pids, err := c.PanePIDs(display)
			assert.NoError(t, err)
			assert.Len(t, pids, 1)
		}()
	}
	wg.Wait()
	assert.Equal(t, 0, runs(), "eight display callers and machine health read the 3 s old list")

	require.True(t, hasPane(t, c, ctx, sibling))
	assert.Equal(t, 1, runs(), "a caller without the option gets a list no older than paneStateTTL")
	ageSnapshot(t, c, 2*time.Second)
	require.True(t, hasPane(t, c, display, sibling))
	assert.Equal(t, 1, runs(), "which the display callers then read")

	ageSnapshot(t, c, DisplayPaneListAge+time.Second)
	require.True(t, hasPane(t, c, display, sibling))
	assert.Equal(t, 2, runs(), "past DisplayPaneListAge a display caller lists again")

	pid, err := c.GetPanePID(ctx, sibling)
	require.NoError(t, err)
	pids, err := c.PanePIDs(display)
	require.NoError(t, err)
	assert.Equal(t, []int{pid}, pids, "PanePIDs is the pane's own pid")
}

// A pane that changes is seen by the next caller that needs it: one Coral starts or ends shows at
// once to the display callers too (the change drops the list); one that changes outside Coral
// shows to a display caller within DisplayPaneListAge, and to any other caller within paneStateTTL.
func TestAChangedPaneIsSeenByTheNextCallerThatNeedsIt(t *testing.T) {
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	display := WithPaneListMaxAge(ctx, DisplayPaneListAge)
	require.True(t, hasPane(t, c, display, sibling))

	mine := "claude-" + strings.Repeat("a", 8) + "-0000-4000-8000-000000000001"
	_, err := c.run(ctx, "new-session", "-d", "-s", mine, "sleep 30")
	require.NoError(t, err)
	assert.True(t, hasPane(t, c, display, mine), "a session Coral started shows at once, even to a display caller")
	_, err = c.run(ctx, "kill-session", "-t", mine)
	require.NoError(t, err)
	assert.False(t, hasPane(t, c, display, mine), "and one it ended is gone at once")

	// A session started outside Coral (another tmux client on the same socket).
	outside := "claude-" + strings.Repeat("b", 8) + "-0000-4000-8000-000000000002"
	out, err := exec.Command(c.resolveTmuxBin(), append(socketArgs(c.SocketPath), "new-session", "-d", "-s", outside, "sleep 30")...).CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() {
		_ = exec.Command(c.resolveTmuxBin(), append(socketArgs(c.SocketPath), "kill-session", "-t", outside)...).Run()
	})
	ageSnapshot(t, c, DisplayPaneListAge-time.Second) // deterministic on a slow runner: still within the age
	assert.False(t, hasPane(t, c, display, outside), "within DisplayPaneListAge a display caller may not see it yet")
	ageSnapshot(t, c, paneStateTTL+100*time.Millisecond)
	assert.False(t, hasPane(t, c, display, outside), "still within DisplayPaneListAge")
	assert.True(t, hasPane(t, c, ctx, outside), "a caller without the option sees it past paneStateTTL")
	assert.True(t, hasPane(t, c, display, outside), "and so does every display caller after it")
}
