package board

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4379: session_joined_at is when the subscription's current session took it: kept while the same
// session stays, renewed when another session takes the role over or the session rejoins after
// leaving; a row from before the column stands on subscribed_at. SessionJoinTimes adds the first task
// claim of a session no longer subscribed.
func TestSessionJoinedAt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const a = "11111111-0000-4000-8000-00000000000a"
	const b = "22222222-0000-4000-8000-00000000000b"
	old := "2020-01-01T00:00:00Z"
	joined := func() string {
		var v *string
		require.NoError(t, s.db.GetContext(ctx, &v, `SELECT session_joined_at FROM board_subscribers WHERE project = 'p' AND subscriber_id = 'Dev'`))
		if v == nil {
			return "<nil>"
		}
		return *v
	}
	set := func(sql string, args ...any) { _, err := s.db.ExecContext(ctx, sql, args...); require.NoError(t, err) }

	_, err := s.Subscribe(ctx, "p", "Dev", "Dev", "claude-"+a, nil, "")
	require.NoError(t, err)
	assert.NotEqual(t, "<nil>", joined(), "set on a new row")

	set(`UPDATE board_subscribers SET session_joined_at = ? WHERE subscriber_id = 'Dev'`, old)
	_, err = s.Subscribe(ctx, "p", "Dev", "Dev", "claude-"+a, nil, "")
	require.NoError(t, err)
	assert.Equal(t, old, joined(), "the same session again: kept")

	_, err = s.Subscribe(ctx, "p", "Dev", "Dev", "claude-"+b, nil, "")
	require.NoError(t, err)
	assert.NotEqual(t, old, joined(), "another session took the role: renewed")

	set(`UPDATE board_subscribers SET session_joined_at = ?, is_active = 0 WHERE subscriber_id = 'Dev'`, old)
	_, err = s.Subscribe(ctx, "p", "Dev", "Dev", "claude-"+b, nil, "")
	require.NoError(t, err)
	assert.NotEqual(t, old, joined(), "rejoined after leaving: renewed")

	set(`UPDATE board_subscribers SET session_joined_at = NULL, subscribed_at = ? WHERE subscriber_id = 'Dev'`, old)
	_, err = s.Subscribe(ctx, "p", "Dev", "Dev", "claude-"+b, nil, "")
	require.NoError(t, err)
	assert.Equal(t, old, joined(), "a legacy row keeps subscribed_at as its join time")

	// SessionJoinTimes: the subscriber's session, and a past session by its first claim.
	task, err := s.CreateTask(ctx, "p", "t", "", "medium", "Operator")
	require.NoError(t, err)
	set(`UPDATE board_tasks SET session_id = ?, claimed_at = ? WHERE id = ?`, a, "2021-06-01T00:00:00Z", task.ID)
	times, err := s.SessionJoinTimes(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), times[b])
	assert.Equal(t, time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC), times[a], "a past session: its first claim")
}
