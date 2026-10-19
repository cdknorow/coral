package board

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4962, the operator: the team health check is OFF for a team that has not turned it on (it uses tokens each time).
func TestHealthCheckIsOffByDefault(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	assert.False(t, DefaultHealthSettings().On)
	assert.False(t, s.GetHealthSettings(ctx, "never-set").On, "a board with no stored setting")
	assert.Empty(t, s.HealthOnByProject(ctx), "nothing stored: nothing in the map")

	require.NoError(t, s.SetHealthOn(ctx, "team", true))
	got := s.GetHealthSettings(ctx, "team")
	assert.True(t, got.On)
	assert.Equal(t, 10, got.IntervalMin, "turning it on keeps the rest as it was (the defaults)")
	assert.True(t, got.ClaimNudge.On, "its rules are on once the check is")
	assert.Equal(t, map[string]bool{"team": true}, s.HealthOnByProject(ctx))

	got.IntervalMin = 30
	require.NoError(t, s.setHealthSettings(ctx, "team", got))
	require.NoError(t, s.SetHealthOn(ctx, "team", false))
	off := s.GetHealthSettings(ctx, "team")
	assert.False(t, off.On)
	assert.Equal(t, 30, off.IntervalMin, "turning it off keeps the operator's numbers")
	assert.Equal(t, map[string]bool{"team": false}, s.HealthOnByProject(ctx))
}

// A team that was running the check when the default became off (it had none stored: on by default) stays on: the first start
// after the change stores "on" for it; a team that had it off keeps that, and a team made after the change has the new default.
func TestHealthDefaultOffKeepsTheTeamsThatHadItOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	ctx := context.Background()
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "had-default", "Lead", "Orchestrator", "claude-a", nil, "all")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "turned-off", "Lead", "Orchestrator", "claude-b", nil, "all")
	require.NoError(t, err)
	require.NoError(t, s.SetHealthOn(ctx, "turned-off", false))
	_, err = s.PostMessage(ctx, "only-messages", "Lead", "hello", nil)
	require.NoError(t, err)
	// The database as an older Coral left it: no marker, and no health setting for a team that never changed it.
	_, err = s.db.ExecContext(ctx, `DELETE FROM board_settings WHERE project = '~store'`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `DELETE FROM board_settings WHERE project IN ('had-default', 'only-messages') AND key = 'health'`)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.True(t, s.GetHealthSettings(ctx, "had-default").On, "a team on by the old default stays on")
	assert.True(t, s.GetHealthSettings(ctx, "only-messages").On, "so does one with only messages")
	assert.False(t, s.GetHealthSettings(ctx, "turned-off").On, "a team that turned it off keeps it off")

	_, err = s.Subscribe(ctx, "made-after", "Lead", "Orchestrator", "claude-c", nil, "all")
	require.NoError(t, err)
	assert.False(t, s.GetHealthSettings(ctx, "made-after").On, "a team made after the change has the new default")

	// A later start does not run it again: the team made after the change is not turned on.
	require.NoError(t, s.Close())
	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.False(t, s.GetHealthSettings(ctx, "made-after").On)
	assert.True(t, s.GetHealthSettings(ctx, "had-default").On)
}
