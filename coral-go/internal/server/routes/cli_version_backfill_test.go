package routes

import (
	"context"
	"os"
	"path/filepath"
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
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir()) // no transcript: the recorded version is the fallback
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

// QA's #3573 note: Claude Code updates itself while an agent runs, so a Claude session's decision
// line takes the version from its transcript's newest line (the running process's own), and the
// recorded version follows it; --version (the binary on disk now) is only the fallback.
func TestGateStateTakesClaudesRunningVersion(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	_, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	var calls atomic.Int32
	h.cliVersionOf = func(map[string]string, string) string { calls.Add(1); return "2.1.999" } // the binary, updated since
	sid := "00000000-0000-4000-8000-000000003574"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: "folder-3574", AgentType: "claude", WorkingDir: t.TempDir()}))
	require.NoError(t, ss.SetCLIVersion(ctx, sid, "2.1.250"))
	id := sid
	_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "folder-3574", SessionID: &id, EventType: "stop", Summary: "done"})
	require.NoError(t, err)
	transcript := filepath.Join(projects, "x", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(transcript), 0o700))
	require.NoError(t, os.WriteFile(transcript, []byte(`{"sessionId":"`+sid+`","version":"2.1.300","type":"user","message":{"content":"a"},"timestamp":"2026-10-15T00:00:00Z"}`+"\n"), 0o600))

	st, ok := h.gateState(ctx, "claude-"+sid, "claude", sid)
	require.True(t, ok)
	assert.Equal(t, "2.1.300", st.CLIVersion, "the running process's version, from its transcript")
	assert.Equal(t, "2.1.300", ss.CLIVersion(ctx, sid), "the recorded version follows it")
	assert.EqualValues(t, 0, calls.Load(), "no --version call while the transcript says")
}
