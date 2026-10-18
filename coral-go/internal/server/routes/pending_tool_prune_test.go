package routes

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #4602 L6: the pending tool of a session that ended (or that is older than its TTL) is dropped by the sweep; a live session's stays.
func TestPendingToolsPrune(t *testing.T) {
	var p pendingTools
	now := time.Now()
	p.set("live", pendingTool{ToolUseID: "a", At: now})
	p.set("gone", pendingTool{ToolUseID: "b", At: now})
	p.set("live-but-stale", pendingTool{ToolUseID: "c", At: now.Add(-pendingToolTTL - time.Minute)})
	n := p.prune(func(id string) bool { return id != "gone" }, now)
	assert.Equal(t, 2, n, "the ended session's and the stale one")
	_, ok := p.get("live", now)
	assert.True(t, ok)
	for _, id := range []string{"gone", "live-but-stale"} {
		p.mu.Lock()
		_, still := p.m[id]
		p.mu.Unlock()
		assert.False(t, still, id)
	}
	assert.Zero(t, (&pendingTools{}).prune(func(string) bool { return false }, now), "an empty set is fine")
}

// The sweep that already drops the other per-session caches drops these too.
func TestPruneSessionCachesDropsPendingTools(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	live, ended := "00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2"
	for _, id := range []string{live, ended} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentName: "a", AgentType: "claude", WorkingDir: "/tmp"}))
		terminal.addSession("claude-"+id, "/tmp")
		h.pending.set(id, pendingTool{ToolUseID: "t", ToolName: "Bash", At: time.Now()})
	}
	require.NoError(t, ss.UnregisterLiveSession(ctx, ended))
	removeMockSession(terminal, "claude-"+ended)
	h.pruneSessionCaches(ctx)
	_, ok := h.pending.get(live, time.Now())
	assert.True(t, ok, "a live session's pending tool stays")
	_, ok = h.pending.get(ended, time.Now())
	assert.False(t, ok, "an ended session's is dropped")
}
