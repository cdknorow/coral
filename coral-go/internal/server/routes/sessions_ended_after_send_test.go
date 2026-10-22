package routes

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// A person's message that could not be delivered, to an agent whose CLI has exited at a plain shell, puts the agent to sleep as Ended at once, not after the reconciler's launch grace (#6058);
// to an agent whose CLI is running it changes nothing.
func TestMarkEndedAfterFailedSend(t *testing.T) {
	server, h, mock, ss := setupSessionsTestServer(t)
	defer server.Close()
	ctx := context.Background()
	const sid = "5b6c7d8e-9f0a-4b1c-8d2e-3f4a5b6c7d8e"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "ended", WorkingDir: "/tmp/ended"}))
	name := naming.SessionName("claude", sid)
	term := newFGTerminal(mock)
	h.terminal = term

	term.setFG(name, "node") // the CLI is running
	assert.False(t, h.markEndedAfterFailedSend(ctx, name, "claude", sid), "a running CLI is not marked")
	ls, err := ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	assert.Zero(t, ls.IsSleeping)

	term.setFG(name, "zsh") // the CLI exited and left its shell
	assert.True(t, h.markEndedAfterFailedSend(ctx, name, "claude", sid))
	ls, err = ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, 1, int(ls.IsSleeping))
	require.NotNil(t, ls.SleepReason)
	assert.Equal(t, store.SleepReasonAgentExited, *ls.SleepReason)

	assert.False(t, h.markEndedAfterFailedSend(ctx, name, "claude", ""), "no session id, nothing to mark")
}
