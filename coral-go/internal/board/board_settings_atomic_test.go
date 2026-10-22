package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5318: an update of several board settings is one transaction. A failure partway (here a trigger that refuses the second write) leaves none of it.
func TestBoardSettingsUpdateIsAllOrNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	off := false // the switches default to on (#5916): the board starts explicitly off, so a half-applied update would show
	_, err := s.UpdateBoardSettings(ctx, "alpha", BoardSettingsUpdate{CrossTeamMessaging: &off, CrossTeamArtifacts: &off})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `CREATE TRIGGER refuse_artifacts BEFORE INSERT ON board_settings WHEN NEW.key = '`+settingCrossTeamArtifacts+`'
		BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	require.NoError(t, err)
	on := true
	_, err = s.UpdateBoardSettings(ctx, "alpha", BoardSettingsUpdate{CrossTeamMessaging: &on, CrossTeamArtifacts: &on})
	require.Error(t, err)
	got, err := s.GetBoardSettings(ctx, "alpha")
	require.NoError(t, err)
	assert.False(t, got.CrossTeamMessaging, "the first setting was not left applied by the failed update")
	assert.False(t, got.CrossTeamArtifacts)
	// Without the failure all of them apply together.
	_, err = s.db.ExecContext(ctx, `DROP TRIGGER refuse_artifacts`)
	require.NoError(t, err)
	servers := []string{"srv-a"}
	got, err = s.UpdateBoardSettings(ctx, "alpha", BoardSettingsUpdate{CrossTeamMessaging: &on, CrossTeamArtifacts: &on, CrossTeamServers: &servers})
	require.NoError(t, err)
	assert.True(t, got.CrossTeamMessaging)
	assert.True(t, got.CrossTeamArtifacts)
	assert.Equal(t, []string{"srv-a"}, got.CrossTeamServers)
}
