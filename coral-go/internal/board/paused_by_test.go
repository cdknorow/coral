package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #286: who paused a board is stored next to its pause, replaced by each pause and removed with it.
func TestPausedByIsStoredAndRemovedWithThePause(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	rows := func() int {
		var n int
		require.NoError(t, s.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM board_settings WHERE project = 'b'`))
		return n
	}

	require.NoError(t, s.SetBoardPausedBy(ctx, "b", true, PausedBySleep))
	got, err := s.PausedBoardsBy(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"b": PausedBySleep}, got)
	assert.Equal(t, 2, rows())

	require.NoError(t, s.SetBoardPaused(ctx, "b", true)) // a person's
	got, err = s.PausedBoardsBy(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"b": ""}, got)
	assert.Equal(t, 1, rows(), "a person's pause has no paused_by row")
	names, err := s.PausedBoards(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, names)

	require.NoError(t, s.SetBoardPausedBy(ctx, "b", true, PausedBySleep))
	require.NoError(t, s.SetBoardPaused(ctx, "b", false))
	got, err = s.PausedBoardsBy(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Zero(t, rows(), "Resume removes the pause and who paused it")
}
