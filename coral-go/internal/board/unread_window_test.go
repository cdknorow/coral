package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// #3875: a first unread count, and the newest-mention searches, look back at most unreadWindow
// of the board's messages, so a never-read subscriber on a long board costs a window, not the
// whole history. Reading still returns every unread message, and a subscriber that receives
// everything is counted exactly.
func TestUnreadWindowBoundsFirstCountsAndSearches(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, b := range []string{"long", "old-only"} {
		_, err := s.Subscribe(ctx, b, "w", "Writer", "tmux-w-"+b, nil, "mentions")
		require.NoError(t, err)
		_, err = s.Subscribe(ctx, b, "lead", "Orchestrator", "tmux-lead-"+b, nil, "all")
		require.NoError(t, err)
	}
	fill := func(board string, n int) {
		tx, err := s.db.Beginx()
		require.NoError(t, err)
		for i := 0; i < n; i++ {
			_, err := tx.Exec(`INSERT INTO board_messages (project, subscriber_id, session_id, content, created_at) VALUES (?, 'x', 'x', 'routine', '2026-10-16T00:00:00Z')`, board)
			require.NoError(t, err)
		}
		require.NoError(t, tx.Commit())
	}
	post := func(board, text string) int64 {
		m, err := s.PostMessage(ctx, board, "x", text, nil)
		require.NoError(t, err)
		return m.ID
	}

	// "long": a mention older than the window, the window's worth of routine posts, a new mention.
	oldID := post("long", "@w an old question")
	fill("long", unreadWindow+100)
	newID := post("long", "@w a new question")
	n, err := s.CheckUnread(ctx, "long", "w")
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the mention inside the window counts")
	counts, err := s.GetAllUnreadCounts(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, counts["tmux-w-long"])
	require.Equal(t, unreadWindow+102, counts["tmux-lead-long"], "receive-all is counted exactly, past the window")
	latest, err := s.LatestUnreadMessageID(ctx, "long", "w")
	require.NoError(t, err)
	require.Equal(t, newID, latest)
	mentioned, err := s.HasUnreadMention(ctx, "long", "w")
	require.NoError(t, err)
	require.True(t, mentioned)

	// Counted once, the counts go on from there.
	post("long", "@w one more")
	n, err = s.CheckUnread(ctx, "long", "w")
	require.NoError(t, err)
	require.Equal(t, 2, n)
	counts, err = s.GetAllUnreadCounts(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, counts["tmux-w-long"])

	// Reading is unchanged: the old mention is still delivered.
	msgs, err := s.ReadMessages(ctx, "long", "w", 50)
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	require.Equal(t, oldID, msgs[0].ID)

	// "old-only": the only mention is older than the window.
	post("old-only", "@w an old question")
	fill("old-only", unreadWindow+10)
	n, err = s.CheckUnread(ctx, "old-only", "w")
	require.NoError(t, err)
	require.Zero(t, n)
	latest, err = s.LatestUnreadMessageID(ctx, "old-only", "w")
	require.NoError(t, err)
	require.Zero(t, latest)
	mentioned, err = s.HasUnreadMention(ctx, "old-only", "w")
	require.NoError(t, err)
	require.False(t, mentioned)
	states, err := s.GetUnreadStatesFor(ctx, []string{"tmux-w-old-only"})
	require.NoError(t, err)
	require.Equal(t, map[string]UnreadState{"tmux-w-old-only": {}}, states, "only the sessions asked for")
}
