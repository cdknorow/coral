package board

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBoundsStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// #4602 M10: a subscriber has at most 50 active waits (one a board); registering again on a board replaces that board's, which
// never counts twice; cancelled, resolved and expired waits do not count and are not deleted.
func TestRegisterWaitIsBoundedPerSubscriber(t *testing.T) {
	ctx := context.Background()
	s := newBoundsStore(t)
	for i := 0; i < MaxActiveWaitsPerSubscriber; i++ {
		_, err := s.RegisterWait(ctx, fmt.Sprintf("board%d", i), "Dev", "tmux-dev", "message", "x", "r", time.Hour)
		require.NoError(t, err, i)
	}
	_, err := s.RegisterWait(ctx, "one-more", "Dev", "tmux-dev", "message", "x", "r", time.Hour)
	assert.ErrorIs(t, err, ErrTooManyWaits)

	_, err = s.RegisterWait(ctx, "board7", "Dev", "tmux-dev", "message", "y", "again", time.Hour)
	assert.NoError(t, err, "a wait on a board that has one replaces it, at the limit")
	_, err = s.RegisterWait(ctx, "one-more", "Someone Else", "tmux-x", "message", "x", "r", time.Hour)
	assert.NoError(t, err, "another subscriber has its own 50")

	require.NoError(t, s.CancelActiveWait(ctx, "board0", "Dev"))
	_, err = s.RegisterWait(ctx, "one-more", "Dev", "tmux-dev", "message", "x", "r", time.Hour)
	assert.NoError(t, err, "a cancelled wait makes room")
	var cancelled int
	require.NoError(t, s.db.Get(&cancelled, `SELECT COUNT(*) FROM board_waits WHERE project = 'board0' AND status = 'cancelled'`))
	assert.Equal(t, 1, cancelled, "and is kept, not deleted")

	// An expired wait is not active.
	_, err = s.db.Exec(`UPDATE board_waits SET expires_at = ? WHERE project = 'board1' AND subscriber_id = 'Dev'`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339))
	require.NoError(t, err)
	_, err = s.RegisterWait(ctx, "after-expiry", "Dev", "tmux-dev", "message", "x", "r", time.Hour)
	assert.NoError(t, err, "an expired wait makes room")
	var rows int
	require.NoError(t, s.db.Get(&rows, `SELECT COUNT(*) FROM board_waits WHERE project = 'board1'`))
	assert.Equal(t, 1, rows, "and its row is still there")
}

func TestCountOpenTasksCreatedBy(t *testing.T) {
	ctx := context.Background()
	s := newBoundsStore(t)
	mk := func(project, by, status string) {
		task, err := s.CreateTaskWithOpts(ctx, project, "t", "", "medium", by, nil)
		require.NoError(t, err)
		_, err = s.db.Exec(`UPDATE board_tasks SET status = ? WHERE id = ?`, status, task.ID)
		require.NoError(t, err)
	}
	for _, st := range []string{"pending", "in_progress", "blocked", "draft", "review_pending"} {
		mk("b", "Lead", st)
	}
	for _, st := range []string{"completed", "skipped"} {
		mk("b", "Lead", st)
	}
	mk("b", "Other", "pending")
	mk("other-board", "Lead", "pending")
	n, err := s.CountOpenTasksCreatedBy(ctx, "b", "Lead")
	require.NoError(t, err)
	assert.Equal(t, 5, n, "the five open states, on this board, by this creator; done ones and others' not")
	n, err = s.CountOpenTasksCreatedBy(ctx, "b", "Nobody")
	require.NoError(t, err)
	assert.Zero(t, n)
}
