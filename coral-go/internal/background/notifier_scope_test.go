package background

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentTo is the sessions rt was asked to type into, in order.
func sentTo(rt *mockRuntime) []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var out []string
	for _, c := range rt.sent {
		out = append(out, c.session)
	}
	return out
}

// twoBoards: Dev on board a and Ops on board b, each tagged in one unread message.
func twoBoards(t *testing.T) (*BoardNotifier, *mockRuntime) {
	t.Helper()
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	n := NewBoardNotifier(bs, rt, time.Hour)
	n.SetIsPausedFn(func(string) bool { return false })
	for _, s := range []struct{ board, id, session string }{
		{"a", "Dev", "claude-aaaa-0001"}, {"a", "PosterA", "claude-aaaa-0002"},
		{"b", "Ops", "claude-bbbb-0001"}, {"b", "PosterB", "claude-bbbb-0002"},
	} {
		_, err := bs.Subscribe(ctx, s.board, s.id, s.id, s.session, nil, "")
		require.NoError(t, err)
	}
	_, err := bs.PostMessage(ctx, "a", "PosterA", "@Dev check this", nil)
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "b", "PosterB", "@Ops check this", nil)
	require.NoError(t, err)
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{
			{AgentName: "dev", AgentType: "claude", SessionID: "aaaa-0001", DisplayName: "Dev"},
			{AgentName: "ops", AgentType: "claude", SessionID: "bbbb-0001", DisplayName: "Ops"},
		}, nil
	})
	return n, rt
}

// #4028 follow-up: a post's pass covers the agents on that board only (a pass over every agent per
// post grew with posts times agents); the timer's full pass still covers every board.
func TestBoardNotifier_APostsPassCoversItsBoardOnly(t *testing.T) {
	n, rt := twoBoards(t)
	ctx := context.Background()

	n.NotifyBoard("a")
	require.NoError(t, n.runPass(ctx, n.takePending()))
	assert.Equal(t, []string{"claude-aaaa-0001"}, sentTo(rt), "board a's agent only")

	require.NoError(t, n.RunOnce(ctx))
	assert.Equal(t, []string{"claude-aaaa-0001", "claude-bbbb-0001"}, sentTo(rt), "the full pass reaches board b, and a isn't nudged twice")
}

// What the next event pass covers: the boards posted to since the last one, together; any
// NotifyNow (or a post with no board) makes it a full pass.
func TestBoardNotifier_PendingBoardsCoalesce(t *testing.T) {
	n := NewBoardNotifier(nil, &mockRuntime{}, time.Hour)
	n.NotifyBoard("a")
	n.NotifyBoard("b")
	n.NotifyBoard("a")
	assert.Equal(t, map[string]bool{"a": true, "b": true}, n.takePending())
	assert.Nil(t, n.takePending(), "nothing pending: a full pass (the timer's)")

	n.NotifyBoard("a")
	n.NotifyNow()
	assert.Nil(t, n.takePending(), "NotifyNow wins: every board")
	n.NotifyBoard("")
	assert.Nil(t, n.takePending(), "no board: every board")
	assert.Len(t, n.notifyNowCh, 1, "one signal for all of them")
}

// A scoped pass sees only some sessions, so it never drops another session's nudge record; the
// full pass prunes the sessions that are gone.
func TestBoardNotifier_OnlyAFullPassForgetsGoneSessions(t *testing.T) {
	n, _ := twoBoards(t)
	ctx := context.Background()
	n.notified["claude-gone-0001"] = notifyState{count: 1, latestID: 1, at: time.Now()}
	require.NoError(t, n.runPass(ctx, map[string]bool{"a": true}))
	_, kept := n.notified["claude-gone-0001"]
	assert.True(t, kept, "a scoped pass keeps it")
	require.NoError(t, n.RunOnce(ctx))
	_, kept = n.notified["claude-gone-0001"]
	assert.False(t, kept, "the full pass forgets it")
}

// The loop runs a post's scoped pass at once, without waiting for the timer.
func TestBoardNotifier_RunServesAScopedPass(t *testing.T) {
	n, rt := twoBoards(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = n.Run(ctx) }()
	n.NotifyBoard("b")
	require.Eventually(t, func() bool { return len(sentTo(rt)) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"claude-bbbb-0001"}, sentTo(rt))
	cancel()
	<-done
}
