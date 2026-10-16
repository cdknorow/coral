package routes

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// Sessions that start and stop leave no per-session cache entries behind:
// after a sweep only live (registered, sleeping included) or still-running
// sessions remain.
func TestPruneSessionCachesKeepsOnlyLiveSessions(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())

	touch := func(id string) {
		h.jsonl.ReadNewMessages(id, "/tmp", "claude") // creates the cache entry
		h.transcriptWarmMu.Lock()
		h.transcriptWarm[id] = true
		h.transcriptWarmMu.Unlock()
	}
	baseline := h.jsonl.CachedSessions()

	// 20 sessions start: registered, running, and read.
	var ids []string
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		ids = append(ids, id)
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentName: "a", AgentType: "claude", WorkingDir: "/tmp"}))
		terminal.addSession("claude-"+id, "/tmp")
		touch(id)
	}
	require.Equal(t, baseline+20, h.jsonl.CachedSessions())
	require.Zero(t, h.pruneSessionCaches(ctx), "nothing ended yet")

	// 15 stop. One of the remaining 5 is only registered (sleeping), one only
	// running in the terminal backend (not registered).
	for _, id := range ids[:15] {
		require.NoError(t, ss.UnregisterLiveSession(ctx, id))
		removeMockSession(terminal, "claude-"+id)
	}
	require.NoError(t, ss.UnregisterLiveSession(ctx, ids[15])) // still running
	removeMockSession(terminal, "claude-"+ids[16])             // still registered
	require.Equal(t, 30, h.pruneSessionCaches(ctx), "15 transcript + 15 warm entries")
	require.Equal(t, baseline+5, h.jsonl.CachedSessions())
	h.transcriptWarmMu.Lock()
	require.Len(t, h.transcriptWarm, 5)
	h.transcriptWarmMu.Unlock()

	// All stop: back to baseline.
	for _, id := range ids[15:] {
		_ = ss.UnregisterLiveSession(ctx, id)
		removeMockSession(terminal, "claude-"+id)
	}
	h.pruneSessionCaches(ctx)
	require.Equal(t, baseline, h.jsonl.CachedSessions())
}

// The sweep is throttled and never runs twice concurrently.
func TestMaybePruneSessionCachesThrottled(t *testing.T) {
	_, h, _, _ := setupSessionsTestServer(t)
	h.maybePruneSessionCaches()
	first := h.cachePrune.last.Load()
	require.NotZero(t, first)
	h.maybePruneSessionCaches()
	require.Equal(t, first, h.cachePrune.last.Load(), "second call within the interval must not start a sweep")
}

func removeMockSession(m *mockSessionTerminal, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, name)
}

// The common path (called by every List, WS build and /status) is two atomic
// loads when a sweep is not due; it takes no lock and does no I/O.
func BenchmarkMaybePruneSessionCachesNotDue(b *testing.B) {
	h := &SessionsHandler{}
	h.cachePrune.last.Store(time.Now().UnixNano())
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			h.maybePruneSessionCaches()
		}
	})
}

// #3999 (the #3994 audit, finding 5): the sync.Map memos keyed by session id are swept of ended
// sessions too, so they do not grow with every session the server ever ran.
func TestPruneSessionCachesSweepsTheSessionMemos(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	live := "00000000-0000-4000-8000-000000000001"
	gone := "00000000-0000-4000-8000-000000000002"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: live, AgentName: "a", AgentType: "claude", WorkingDir: "/tmp"}))
	terminal.addSession("claude-"+live, "/tmp")
	for _, id := range []string{live, gone} {
		h.cliVersionTried.Store(id, true)
		h.hooksRev.Store(id, 3)
		h.hooksNoRev.Store(id, true)
	}
	require.Equal(t, 3, h.pruneSessionCaches(ctx), "the ended session's three memos")
	for _, m := range []*sync.Map{&h.cliVersionTried, &h.hooksRev, &h.hooksNoRev} {
		_, hasLive := m.Load(live)
		_, hasGone := m.Load(gone)
		require.True(t, hasLive)
		require.False(t, hasGone)
	}
}
