package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Lists that show the order things were done in must not take it from RFC3339Nano text: it drops trailing zeros, so ".12Z" (120 ms) sorts after ".123Z" (123 ms). These force two stamps that sort
// the wrong way round and require the write order (see timestamp_order_lint_test.go for the rule that keeps a new list from being written this way).
func TestSavedBookmarksAreListedNewestFirstWhateverTheirStampsSortAs(t *testing.T) {
	ctx := context.Background()
	ss := NewSessionStore(openTestDB(t))
	a, err := ss.SaveMessage(ctx, SavedMessage{Kind: "agent", Ref: "s1", Key: "k1"})
	require.NoError(t, err)
	b, err := ss.SaveMessage(ctx, SavedMessage{Kind: "agent", Ref: "s1", Key: "k2"})
	require.NoError(t, err)
	_, err = ss.DB().ExecContext(ctx, `UPDATE saved_messages SET created_at = '2026-10-22T20:00:00.12Z' WHERE id = ?`, a.ID)
	require.NoError(t, err)
	_, err = ss.DB().ExecContext(ctx, `UPDATE saved_messages SET created_at = '2026-10-22T20:00:00.123Z' WHERE id = ?`, b.ID)
	require.NoError(t, err)
	got, err := ss.ListSaved(ctx, "", "", "", SavedScope{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"k2", "k1"}, []string{got[0].Key, got[1].Key}, "the later bookmark is first")
}

func TestPinnedFoldersAreListedOldestPinFirstWhateverTheirStampsSortAs(t *testing.T) {
	ctx := context.Background()
	ss := NewSessionStore(openTestDB(t))
	require.NoError(t, ss.PinFolder(ctx, "/work/zeta"))
	require.NoError(t, ss.PinFolder(ctx, "/work/alpha"))
	_, err := ss.DB().ExecContext(ctx, `UPDATE pinned_folders SET pinned_at = '2026-10-22T20:00:00.12Z' WHERE path = '/work/zeta'`)
	require.NoError(t, err)
	_, err = ss.DB().ExecContext(ctx, `UPDATE pinned_folders SET pinned_at = '2026-10-22T20:00:00.123Z' WHERE path = '/work/alpha'`)
	require.NoError(t, err)
	got, err := ss.ListPinnedFolders(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"/work/zeta", "/work/alpha"}, []string{got[0].Path, got[1].Path}, "the first pin is first, not the one whose stamp text sorts first")
}
