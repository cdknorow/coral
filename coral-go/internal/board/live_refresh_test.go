package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLiveRefreshUnreadScopeAndCancellation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, agent := range []string{"awake", "sleeping", "sender"} {
		_, err := sub(s, ctx, "project", agent, "Developer")
		require.NoError(t, err)
	}
	_, err := sub(s, ctx, "other", "other-agent", "Developer")
	require.NoError(t, err)
	_, err = s.PostMessage(ctx, "project", "sender", "@all hello", nil)
	require.NoError(t, err)
	counts, err := s.GetUnreadCountsForSessions(ctx, []string{"tmux-awake"})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"tmux-awake": 1}, counts)
	counts, err = s.GetUnreadCountsForSessions(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, counts)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.GetUnreadCountsForSessions(canceled, []string{"tmux-awake"})
	require.Error(t, err, "DB errors must not silently become zero unread")
}

func TestLiveRefreshUnreadModesAndCursors(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, name := range []string{"all-reader", "mentions-reader", "group-reader", "none-reader", "sender"} {
		_, err := sub(s, ctx, "project", name, "Developer")
		require.NoError(t, err)
	}
	for name, mode := range map[string]string{"all-reader": "all", "mentions-reader": "mentions", "group-reader": "group", "none-reader": "none"} {
		_, err := s.db.Exec("UPDATE board_subscribers SET receive_mode=? WHERE subscriber_id=?", mode, name)
		require.NoError(t, err)
	}
	require.NoError(t, s.AddToGroup(ctx, "project", "group", "sender"))
	old, err := s.PostMessage(ctx, "project", "sender", "old", nil)
	require.NoError(t, err)
	_, err = s.db.Exec("UPDATE board_subscribers SET last_read_id=? WHERE subscriber_id='all-reader'", old.ID)
	require.NoError(t, err)
	_, err = s.PostMessage(ctx, "project", "sender", "@mentions-reader latest", nil)
	require.NoError(t, err)
	_, err = s.PostMessage(ctx, "project", "all-reader", "own message", nil)
	require.NoError(t, err)
	_, err = s.PostMessage(ctx, "project", "Coral Task Queue", "@all queue", nil)
	require.NoError(t, err)
	names := []string{"tmux-all-reader", "tmux-mentions-reader", "tmux-group-reader", "tmux-none-reader"}
	counts, err := s.GetUnreadCountsForSessions(ctx, names)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"tmux-all-reader": 1, "tmux-mentions-reader": 1, "tmux-group-reader": 2, "tmux-none-reader": 0}, counts)
}
