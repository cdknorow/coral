package background

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
)

// #6544: a nudge held for an agent is dropped when its process is replaced (Sleep/Wake), but the
// notifier kept "already notified" and a mentions-mode agent got no nudge until the reminder window.
func TestBoardNotifier_ProcessReplacedNudgesAgainAtOnce(t *testing.T) {
	n, rt := twoBoards(t)
	ctx := context.Background()
	defer n.watchResets()()

	require.NoError(t, n.RunOnce(ctx))
	require.Equal(t, []string{"claude-aaaa-0001", "claude-bbbb-0001"}, sentTo(rt))

	require.NoError(t, n.RunOnce(ctx))
	assert.Len(t, sentTo(rt), 2, "a nudge that went out is not repeated inside the reminder window")

	ptymanager.DropHeld("claude-aaaa-0001", "spawned") // Wake: a new process
	require.NoError(t, n.RunOnce(ctx))
	assert.Equal(t, []string{"claude-aaaa-0001", "claude-bbbb-0001", "claude-aaaa-0001"}, sentTo(rt), "the woken agent is nudged on the next pass, and only it")
}

func TestBoardNotifier_NewMessageAfterADropNudges(t *testing.T) {
	n, rt := twoBoards(t)
	ctx := context.Background()
	defer n.watchResets()()
	require.NoError(t, n.RunOnce(ctx))
	base := len(sentTo(rt))

	ptymanager.DropHeld("claude-aaaa-0001", "killed")
	_, err := n.boardStore.PostMessage(ctx, "a", "PosterA", "probe 2, no tag", nil)
	require.NoError(t, err)
	require.NoError(t, n.RunOnce(ctx))
	assert.Equal(t, base+1, len(sentTo(rt)), "the message after the drop is nudged at once, not after the window")
}

func TestBoardNotifier_StoppedWatchIgnoresDrops(t *testing.T) {
	n, rt := twoBoards(t)
	ctx := context.Background()
	stop := n.watchResets()
	stop()
	require.NoError(t, n.RunOnce(ctx))
	base := len(sentTo(rt))
	ptymanager.DropHeld("claude-aaaa-0001", "spawned")
	n.now = func() time.Time { return time.Now() }
	require.NoError(t, n.RunOnce(ctx))
	assert.Equal(t, base, len(sentTo(rt)), "a stopped notifier no longer listens")
}
