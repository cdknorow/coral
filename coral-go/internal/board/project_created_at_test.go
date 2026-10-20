package board

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func projectByID(t *testing.T, s *Store, id string) (ProjectInfo, bool) {
	t.Helper()
	list, err := s.ListProjects(context.Background())
	require.NoError(t, err)
	for _, p := range list {
		if p.Project == id {
			return p, true
		}
	}
	return ProjectInfo{}, false
}

// #5236: GET /api/board/projects tells a board deleted and created again under the same name from the board it replaced: every board carries board_id (the same value
// as project) and created_at (the boards row's own, RFC 3339 UTC), and a re-created board gets a new created_at.
func TestListProjectsCarriesBoardIDAndCreatedAt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "alpha", "Dev", "Developer", "claude-aaaaaaaa-0000-0000-0000-000000000000", nil, "all")
	require.NoError(t, err)
	p, ok := projectByID(t, s, "alpha")
	require.True(t, ok)
	assert.Equal(t, "alpha", p.BoardID, "board_id is project")
	first, err := time.Parse(time.RFC3339, p.CreatedAt)
	require.NoError(t, err, "RFC 3339: %q", p.CreatedAt)
	assert.WithinDuration(t, time.Now().UTC(), first, time.Minute)
	b, err := s.GetBoard(ctx, "alpha")
	require.NoError(t, err)
	assert.Equal(t, b.CreatedAt, p.CreatedAt, "what the boards row holds")

	// Delete it and use the name again: a new board, a new created_at. (The old row is dated in the past: created_at has second resolution.)
	_, err = s.db.ExecContext(ctx, `UPDATE boards SET created_at = '2026-01-01T00:00:00Z' WHERE id = 'alpha'`)
	require.NoError(t, err)
	old, _ := projectByID(t, s, "alpha")
	require.Equal(t, "2026-01-01T00:00:00Z", old.CreatedAt)
	require.NoError(t, s.DeleteProject(ctx, "alpha"))
	_, gone := projectByID(t, s, "alpha")
	assert.False(t, gone)
	_, err = s.Subscribe(ctx, "alpha", "Dev", "Developer", "claude-bbbbbbbb-0000-0000-0000-000000000000", nil, "all")
	require.NoError(t, err)
	again, ok := projectByID(t, s, "alpha")
	require.True(t, ok)
	assert.NotEqual(t, old.CreatedAt, again.CreatedAt, "a board deleted and created again under the same name is told apart")
	second, err := time.Parse(time.RFC3339, again.CreatedAt)
	require.NoError(t, err)
	assert.True(t, second.After(first.Add(-time.Second)))

	// A board in use with no boards row (none after a start): created_at is "".
	_, err = s.db.ExecContext(ctx, `DELETE FROM boards WHERE id = 'alpha'`)
	require.NoError(t, err)
	bare, ok := projectByID(t, s, "alpha")
	require.True(t, ok)
	assert.Equal(t, "", bare.CreatedAt)
	assert.Equal(t, "alpha", bare.BoardID)
}
