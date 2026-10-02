package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoteBoardStoreCRUDOnFreshAndRepeatedOpen(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	remote := NewRemoteBoardStore(db)

	sub, err := remote.AddRemoteSub(ctx, "session-1", "https://boards.example", "team", "Agent")
	require.NoError(t, err)
	require.Equal(t, "session-1", sub.SessionID)
	require.Equal(t, 0, sub.LastNotifiedUnread)

	subs, err := remote.ListAllRemoteSubs(ctx)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.NoError(t, remote.UpdateLastNotified(ctx, sub.ID, 3))

	subs, err = remote.ListAllRemoteSubs(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, subs[0].LastNotifiedUnread)

	// Re-running schema setup must preserve the retained row and remain safe.
	require.NoError(t, db.ensureSchema(ctx))
	subs, err = remote.ListAllRemoteSubs(ctx)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.NoError(t, db.Close())
}
