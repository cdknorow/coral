package board

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedUnreadBoard mirrors the notifier load-test seed: one board, an
// Orchestrator in "all" mode and Developers in "mentions" mode, none of whom
// has read the n history messages.
func seedUnreadBoard(tb testing.TB, n int) *Store {
	tb.Helper()
	ctx := context.Background()
	s, err := NewStore(filepath.Join(tb.TempDir(), "board.db"))
	require.NoError(tb, err)
	tb.Cleanup(func() { s.Close() })
	_, err = s.Subscribe(ctx, "team", "Orchestrator", "Orchestrator", "claude-orch", nil, "all")
	require.NoError(tb, err)
	for i := 0; i < 6; i++ {
		_, err = s.Subscribe(ctx, "team", fmt.Sprintf("Agent %02d", i), "Developer", fmt.Sprintf("claude-%d", i), nil, "mentions")
		require.NoError(tb, err)
	}
	tx, err := s.db.Beginx()
	require.NoError(tb, err)
	for i := 0; i < n; i++ {
		sender := "Agent 07"
		if i%50 == 0 {
			sender = "Coral Health Monitor"
		}
		_, err = tx.Exec(`INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at) VALUES ('team', ?, ?, ?, '2026-01-01T00:00:00Z')`,
			sender, sender, fmt.Sprintf("history message %d @Agent %02d progress update with some body text", i, i%20))
		require.NoError(tb, err)
	}
	require.NoError(tb, tx.Commit())
	return s
}

func BenchmarkNotifierUnreadQueries(b *testing.B) {
	s := seedUnreadBoard(b, 10000)
	ctx := context.Background()
	for _, sub := range []string{"Orchestrator", "Agent 03"} {
		b.Run(sub+"/CheckUnread", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = s.CheckUnread(ctx, "team", sub)
			}
		})
		b.Run(sub+"/LatestUnreadMessageID", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = s.LatestUnreadMessageID(ctx, "team", sub)
			}
		})
		b.Run(sub+"/HasUnreadHealthMessage", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = s.HasUnreadHealthMessage(ctx, "team", sub)
			}
		})
		b.Run(sub+"/HasUnreadNonHealthMessage", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = s.HasUnreadNonHealthMessage(ctx, "team", sub)
			}
		})
	}
}
