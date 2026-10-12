package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// GetLiveDisplayNames agrees with GetLiveSession(...).DisplayName, the
// per-agent lookup it replaces in the background services' discovery.
func TestGetLiveDisplayNamesMatchesGetLiveSession(t *testing.T) {
	ss := NewSessionStore(openTestDB(t))
	ctx := context.Background()
	named, unnamed := "Backend Dev", ""
	for _, ls := range []*LiveSession{
		{SessionID: "s-named", AgentType: "claude", AgentName: "repo", WorkingDir: "/r", DisplayName: &named},
		{SessionID: "s-plain", AgentType: "claude", AgentName: "repo", WorkingDir: "/r"},
		{SessionID: "s-empty", AgentType: "claude", AgentName: "repo", WorkingDir: "/r", DisplayName: &unnamed},
	} {
		require.NoError(t, ss.RegisterLiveSession(ctx, ls))
	}
	ids := []string{"s-named", "s-plain", "s-empty", "s-missing"}
	got, err := ss.GetLiveDisplayNames(ctx, ids)
	require.NoError(t, err)
	for _, id := range ids {
		ls, err := ss.GetLiveSession(ctx, id)
		require.NoError(t, err)
		want, wantOK := "", false
		if ls != nil && ls.DisplayName != nil {
			want, wantOK = *ls.DisplayName, true
		}
		name, ok := got[id]
		require.Equal(t, wantOK, ok, id)
		require.Equal(t, want, name, id)
	}
	require.Equal(t, "Backend Dev", got["s-named"])
	empty, err := ss.GetLiveDisplayNames(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
