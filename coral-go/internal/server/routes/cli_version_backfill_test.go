package routes

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #3573: a session launched before its CLI version was recorded gets one, at the server's start
// (the backfill) or when the gate first looks at it (in the background, once), so the shadow
// decision lines carry cli_version.
func TestCLIVersionBackfill(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	var calls atomic.Int32
	h.cliVersionOf = func(_ map[string]string, agentType string) string {
		calls.Add(1)
		if agentType == "claude" {
			return "2.1.300"
		}
		return ""
	}
	for _, s := range []store.LiveSession{
		{SessionID: "old-claude", AgentName: "a", AgentType: "claude", WorkingDir: t.TempDir()},
		{SessionID: "known", AgentName: "b", AgentType: "claude", WorkingDir: t.TempDir()},
		{SessionID: "typeless", AgentName: "c", WorkingDir: t.TempDir()},
	} {
		s := s
		require.NoError(t, ss.RegisterLiveSession(ctx, &s))
	}
	require.NoError(t, ss.SetCLIVersion(ctx, "known", "1.0.0"))

	h.BackfillCLIVersions(ctx)
	assert.Equal(t, "2.1.300", ss.CLIVersion(ctx, "old-claude"))
	assert.Equal(t, "1.0.0", ss.CLIVersion(ctx, "known"), "a recorded version is left alone")
	assert.Equal(t, "", ss.CLIVersion(ctx, "typeless"), "no agent type, no CLI to ask")
	assert.EqualValues(t, 1, calls.Load(), "only the session missing a version is looked up")

	h.BackfillCLIVersions(ctx)
	assert.EqualValues(t, 1, calls.Load(), "a session is looked up once")
}

func TestGateStateFillsAMissingCLIVersion(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	var calls atomic.Int32
	h.cliVersionOf = func(map[string]string, string) string { calls.Add(1); return "2.1.300" }
	sid := "00000000-0000-4000-8000-000000003573"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: "folder-3573", AgentType: "claude", WorkingDir: t.TempDir()}))
	id := sid
	_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "folder-3573", SessionID: &id, EventType: "stop", Summary: "done"})
	require.NoError(t, err)

	st, ok := h.gateState(ctx, "claude-"+sid, "claude", sid)
	require.True(t, ok, "the session has signals")
	assert.Equal(t, "", st.CLIVersion, "the first look finds none and asks in the background")
	require.Eventually(t, func() bool { return ss.CLIVersion(ctx, sid) == "2.1.300" }, 5*time.Second, 10*time.Millisecond)

	st, ok = h.gateState(ctx, "claude-"+sid, "claude", sid)
	require.True(t, ok)
	assert.Equal(t, "2.1.300", st.CLIVersion, "the next decision line has the version")
	assert.EqualValues(t, 1, calls.Load(), "looked up once")
}
