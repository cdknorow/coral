package startup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// QA #6993: a session whose only stored events are status events did start, so the startup settle must not read it as "no event at all" (agent_never_started).
func TestLastEventOfRunDoesNotCallAStatusOnlySessionNeverStarted(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ts := store.NewTaskStore(db)
	sid := "sid-status-only"
	_, err = ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "a", SessionID: &sid, EventType: "status", Summary: "working"})
	require.NoError(t, err)
	any, err := ts.SessionsWithAnyEvent(ctx, []string{sid})
	require.NoError(t, err)
	require.True(t, any[sid], "the session has an event")
	last, failed := lastEventOfRun(ctx, ts, sid)
	assert.False(t, last == "" && !failed, "settle reads this session as having reported nothing at all")
}
