package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A restart or resume gives the new session the old one's tags. The old rows
// stay only while a history entry shows them (history lists tags by session
// id); otherwise they would be orphans (#3273).
func TestMigrateSessionTagsKeepsHistoryAndDropsOrphans(t *testing.T) {
	s := NewSessionStore(openTestDB(t))
	ctx := context.Background()
	tag, err := s.CreateTag(ctx, "frontend", "")
	require.NoError(t, err)

	require.NoError(t, s.AddSessionTag(ctx, "live-only", tag.ID))
	require.NoError(t, s.MigrateSessionTags(ctx, "live-only", "next-1"))
	got, err := s.GetSessionTags(ctx, "next-1")
	require.NoError(t, err)
	assert.Len(t, got, 1)
	old, err := s.GetSessionTags(ctx, "live-only")
	require.NoError(t, err)
	assert.Empty(t, old, "no history entry: the old rows are orphans and go")

	require.NoError(t, s.UpsertSessionIndex(ctx, &SessionIndex{SessionID: "in-history", SourceType: "claude", SourceFile: "/h/in-history.jsonl", FileMtime: 1}))
	require.NoError(t, s.AddSessionTag(ctx, "in-history", tag.ID))
	require.NoError(t, s.MigrateSessionTags(ctx, "in-history", "next-2"))
	old, err = s.GetSessionTags(ctx, "in-history")
	require.NoError(t, err)
	assert.Len(t, old, 1, "history still shows the old session with its tags")
	got, err = s.GetSessionTags(ctx, "next-2")
	require.NoError(t, err)
	assert.Len(t, got, 1)

	// The same id (a defensive no-op) never loses its tags.
	require.NoError(t, s.MigrateSessionTags(ctx, "next-1", "next-1"))
	got, err = s.GetSessionTags(ctx, "next-1")
	require.NoError(t, err)
	assert.Len(t, got, 1)
}
