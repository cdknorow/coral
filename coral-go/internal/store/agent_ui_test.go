package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentUIPersistenceAndEventCursor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ui.db")
	db, err := Open(path)
	require.NoError(t, err)
	p, err := db.PutAgentUI(ctx, "session", "diagram", "Diagram", "<svg/>")
	require.NoError(t, err)
	id, err := db.AddAgentUIEvent(ctx, "session", "diagram", p.Revision, "choose", `{"value":1}`)
	require.NoError(t, err)
	count, err := db.AgentUIEventCount(ctx, "session", "diagram")
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Close())
	db, err = Open(path)
	require.NoError(t, err)
	defer db.Close()
	got, err := db.GetAgentUI(ctx, "session", "diagram")
	require.NoError(t, err)
	require.Equal(t, p, got)
	events, err := db.AgentUIEvents(ctx, "session", "diagram", 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, id, events[0].ID)
	events, err = db.AgentUIEvents(ctx, "session", "diagram", id)
	require.NoError(t, err)
	require.Empty(t, events)
	_, err = db.AddAgentUIEvent(ctx, "other", "diagram", 1, "choose", `null`)
	require.ErrorIs(t, err, ErrUIStale)
}
