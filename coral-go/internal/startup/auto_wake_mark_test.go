package startup

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3636: only Coral's own stop and start mark an agent coral_stopped (the auto-wake's set).

func markRig(t *testing.T) (*store.DB, *store.SessionStore) {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	for _, id := range []string{"awake-1", "awake-2", "operator-slept"} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id + "-0000-4000-8000-000000000000", AgentName: id, AgentType: "claude", WorkingDir: t.TempDir()}))
	}
	require.NoError(t, ss.SetSessionSleeping(ctx, "operator-slept-0000-4000-8000-000000000000", true))
	return db, ss
}

func reasons(t *testing.T, ss *store.SessionStore) map[string]string {
	t.Helper()
	all, err := ss.GetAllLiveSessions(context.Background())
	require.NoError(t, err)
	out := map[string]string{}
	for _, ls := range all {
		r := ""
		if ls.SleepReason != nil {
			r = *ls.SleepReason
		}
		out[ls.AgentName] = r
	}
	return out
}

func TestPTYShutdownMarksRunningAgentsCoralStopped(t *testing.T) {
	db, ss := markRig(t)
	rs := &RunningServer{HTTPServer: &http.Server{}, DB: db, Backend: ptymanager.NewPTYBackend()}
	rs.Shutdown(5 * time.Second)
	got := reasons(t, ss)
	assert.Equal(t, store.SleepReasonCoralStopped, got["awake-1"])
	assert.Equal(t, store.SleepReasonCoralStopped, got["awake-2"])
	assert.Equal(t, "", got["operator-slept"], "the Operator's Sleep is left as it was")
}

func TestReconcileMarksCrashedAgentsOnPTYOnly(t *testing.T) {
	db, ss := markRig(t)
	rt := background.NewPTYRuntime(ptymanager.NewPTYBackend()) // nothing running: every awake row crashed
	reconcileOrphanedSessions(context.Background(), db, rt, true)
	got := reasons(t, ss)
	assert.Equal(t, store.SleepReasonCoralStopped, got["awake-1"])
	assert.Equal(t, "", got["operator-slept"])

	db2, ss2 := markRig(t)
	reconcileOrphanedSessions(context.Background(), db2, rt, false)
	got = reasons(t, ss2)
	assert.Equal(t, "", got["awake-1"], "on tmux a crashed agent is not marked for the auto-wake")
}
