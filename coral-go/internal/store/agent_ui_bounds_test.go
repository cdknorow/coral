package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openUIDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenWithKey(context.Background(), filepath.Join(t.TempDir(), "ui.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// #4602 M8: a session has at most 20 panels; replacing one never counts as a new one, and another session's panels are not counted.
func TestAgentUIPanelCountIsBounded(t *testing.T) {
	ctx := context.Background()
	db := openUIDB(t)
	for i := 0; i < MaxAgentUIPanels; i++ {
		_, err := db.PutAgentUI(ctx, "s1", fmt.Sprintf("p%d", i), "t", "<p/>")
		require.NoError(t, err, i)
	}
	_, err := db.PutAgentUI(ctx, "s1", "one-too-many", "t", "<p/>")
	assert.ErrorIs(t, err, ErrUIPanelLimit)
	got, err := db.GetAgentUI(ctx, "s1", "one-too-many")
	require.NoError(t, err)
	assert.Nil(t, got, "nothing was stored")

	p, err := db.PutAgentUI(ctx, "s1", "p3", "new title", "<b/>") // a replacement is allowed at the limit
	require.NoError(t, err)
	assert.EqualValues(t, 2, p.Revision)
	_, err = db.PutAgentUI(ctx, "s2", "p0", "t", "<p/>")
	assert.NoError(t, err, "another session has its own 20")

	require.NoError(t, db.DeleteAgentUI(ctx, "s1", "p0"))
	_, err = db.PutAgentUI(ctx, "s1", "one-too-many", "t", "<p/>")
	assert.NoError(t, err, "a removed panel makes room")
}

// The HTML of all of a session's panels is at most 32 MiB; a replaced panel's own HTML is not counted twice, and bytes (not characters) are.
func TestAgentUIHTMLTotalIsBounded(t *testing.T) {
	ctx := context.Background()
	db := openUIDB(t)
	big := strings.Repeat("a", 2<<20)
	for i := 0; i < 16; i++ { // 16 x 2 MiB = 32 MiB: exactly the limit
		_, err := db.PutAgentUI(ctx, "s1", fmt.Sprintf("big%d", i), "t", big)
		require.NoError(t, err, i)
	}
	_, err := db.PutAgentUI(ctx, "s1", "extra", "t", "x")
	assert.ErrorIs(t, err, ErrUIHTMLLimit, "one byte over the 32 MiB")
	_, err = db.PutAgentUI(ctx, "s1", "big0", "t", big) // the same size again, in place
	assert.NoError(t, err, "replacing a panel with one of the same size stays within the limit")
	_, err = db.PutAgentUI(ctx, "s1", "big0", "t", big+"x")
	assert.ErrorIs(t, err, ErrUIHTMLLimit, "a replacement one byte bigger is over")
	p, err := db.PutAgentUI(ctx, "s1", "big0", "t", "tiny")
	require.NoError(t, err)
	assert.EqualValues(t, 3, p.Revision, "a smaller replacement frees room")
	_, err = db.PutAgentUI(ctx, "s1", "extra", "t", strings.Repeat("b", 2<<20-4))
	assert.NoError(t, err, "and the room is usable")
	_, err = db.PutAgentUI(ctx, "s2", "p", "t", big)
	assert.NoError(t, err, "another session is counted on its own")

	// Multi-byte characters count as bytes: 3 bytes each.
	_, err = db.PutAgentUI(ctx, "s3", "bytes", "t", strings.Repeat("€", 700_000)) // 2.1 MB
	require.NoError(t, err)
	for i := 0; i < 14; i++ {
		_, err = db.PutAgentUI(ctx, "s3", fmt.Sprintf("m%d", i), "t", strings.Repeat("€", 700_000))
		require.NoError(t, err, i)
	}
	_, err = db.PutAgentUI(ctx, "s3", "last", "t", strings.Repeat("€", 700_000))
	assert.ErrorIs(t, err, ErrUIHTMLLimit, "16 x 2.1 MB of multi-byte text is over 32 MiB in bytes though it is under in characters")
}

// A panel keeps its newest 1,000 events; the oldest go on insert, in order, and the cursor still works.
func TestAgentUIEventsAreBoundedToTheNewest(t *testing.T) {
	ctx := context.Background()
	db := openUIDB(t)
	p, err := db.PutAgentUI(ctx, "s1", "p", "t", "<p/>")
	require.NoError(t, err)
	other, err := db.PutAgentUI(ctx, "s1", "other", "t", "<p/>")
	require.NoError(t, err)
	var ids []int64
	for i := 0; i < MaxAgentUIEvents+25; i++ {
		id, err := db.AddAgentUIEvent(ctx, "s1", "p", p.Revision, "click", fmt.Sprintf(`{"n":%d}`, i))
		require.NoError(t, err)
		ids = append(ids, id)
	}
	_, err = db.AddAgentUIEvent(ctx, "s1", "other", other.Revision, "click", `{"n":0}`)
	require.NoError(t, err)

	n, err := db.AgentUIEventCount(ctx, "s1", "p")
	require.NoError(t, err)
	assert.EqualValues(t, MaxAgentUIEvents, n)
	n, err = db.AgentUIEventCount(ctx, "s1", "other")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n, "another panel is untouched")
	first, err := db.AgentUIEvents(ctx, "s1", "p", 0)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	assert.Equal(t, ids[25], first[0].ID, "the 25 oldest went")
	assert.JSONEq(t, `{"n":25}`, first[0].Payload)
	after, err := db.AgentUIEvents(ctx, "s1", "p", ids[len(ids)-3])
	require.NoError(t, err)
	assert.Len(t, after, 2, "the cursor still reads the newest")

	// Exactly the limit prunes nothing.
	q, err := db.PutAgentUI(ctx, "s2", "p", "t", "<p/>")
	require.NoError(t, err)
	for i := 0; i < MaxAgentUIEvents; i++ {
		_, err = db.AddAgentUIEvent(ctx, "s2", "p", q.Revision, "click", `{}`)
		require.NoError(t, err)
	}
	n, _ = db.AgentUIEventCount(ctx, "s2", "p")
	assert.EqualValues(t, MaxAgentUIEvents, n)
}
