package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4792: a bookmark saved before the team and the author were kept belongs to its board's team once the database is opened again; an agent's has none to give.
func TestSavedBookmarksFromBeforeTheTeamWasKeptTakeTheirBoard(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	_, err := db.ExecContext(ctx, `INSERT INTO saved_messages (kind, ref, msg_key, created_at) VALUES ('board', 'old-team', '5', '2026-10-01T00:00:00Z'), ('agent', 'sess-1', 'a1a1a1a1a1a1a1a1a1a1', '2026-10-01T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, db.ensureSchema(ctx))
	ss := NewSessionStore(db)
	got, err := ss.ListSaved(ctx, "", "", "", SavedScope{Team: "old-team"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "5", got[0].Key)
	assert.Equal(t, "", got[0].Author, "an author not known yet reads as empty")
	unknown, err := ss.UnknownSavedAuthors(ctx, "old-team")
	require.NoError(t, err)
	assert.Len(t, unknown, 1, "and is waiting to be looked up")
	all, err := ss.ListSaved(ctx, "", "", "", SavedScope{})
	require.NoError(t, err)
	assert.Len(t, all, 2, "the agent's bookmark is still in everything")
	require.NoError(t, ss.SetSavedAuthor(ctx, got[0].ID, ""))
	unknown, _ = ss.UnknownSavedAuthors(ctx, "old-team")
	assert.Empty(t, unknown, "an author found missing is not asked again")
}
