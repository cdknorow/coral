package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// A state change is on /ws/coral at once, not on the next tick: archive and
// unarchive reach a dashboard and a hub feed as a coral_diff well inside the
// poll interval (set to 10s here, and the test keeps clear of the next wall-clock tick, so the
// ticker cannot be what delivers it).
// A burst of kicks costs one shared build.
func TestWSCoral_KickPushesStateChangesImmediately(t *testing.T) {
	var builds atomic.Int64

	server, handler := setupTestServer(t)
	handler.wsBuildHook = func() { builds.Add(1) }
	handler.cfg.WSPollInterval = 10 * time.Second
	ctx := context.Background()
	ss := store.NewSessionStore(handler.db)
	const sid = "7a1b2c3d-0000-4000-8000-00000000c0de"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: "kick-agent", WorkingDir: "/tmp/test", IsSleeping: 1,
	}))
	routes := chi.NewRouter()
	routes.Post("/api/sessions/live/{sessionID}/archive", handler.ArchiveSession)
	routes.Post("/api/sessions/live/{sessionID}/unarchive", handler.UnarchiveSession)
	post := func(action string) {
		t.Helper()
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+sid+"/"+action, nil))
		require.Equal(t, http.StatusOK, w.Code)
	}

	wsCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var conns []*websocket.Conn
	for _, path := range []string{"/ws/coral", "/ws/coral?feed=hub"} {
		conn, _, err := websocket.Dial(wsCtx, "ws"+server.URL[4:]+path, nil)
		require.NoError(t, err)
		t.Cleanup(func() { conn.CloseNow() })
		var first struct {
			Type string `json:"type"`
		}
		require.NoError(t, wsjson.Read(wsCtx, conn, &first))
		require.Equal(t, "coral_update", first.Type)
		conns = append(conns, conn)
	}
	// No tick during the archive round trips and the burst: a tick would deliver a change
	// (the kick must) and add a build to the count (#3932).
	clearOfTick(handler.cfg.WSPollInterval, 5*time.Second)

	// awaitArchived reads conn until a coral_diff carries sid with archived=want,
	// failing if that takes longer than a generous bound (the tick is 10s away).
	awaitArchived := func(conn *websocket.Conn, want bool) time.Duration {
		t.Helper()
		start := time.Now()
		readCtx, stop := context.WithTimeout(wsCtx, 2*time.Second)
		defer stop()
		for {
			var msg struct {
				Type    string           `json:"type"`
				Changed []map[string]any `json:"changed"`
			}
			require.NoError(t, wsjson.Read(readCtx, conn, &msg), "no coral_diff with archived=%v before the tick", want)
			for _, e := range msg.Changed {
				if e["session_id"] == sid && e["archived"] == want {
					return time.Since(start)
				}
			}
		}
	}
	for _, want := range []bool{true, false} {
		action := "archive"
		if !want {
			action = "unarchive"
		}
		post(action)
		for i, conn := range conns {
			took := awaitArchived(conn, want)
			require.Less(t, took, 200*time.Millisecond, "client %d got archived=%v after %v", i, want, took)
		}
	}

	// A burst of kicks spread over ~30ms (a UI archiving agents one request at a
	// time): both connections rebuild once, sharing one build.
	before := builds.Load()
	burstStart := time.Now()
	for range 10 {
		handler.kickWS()
		time.Sleep(3 * time.Millisecond)
	}
	// The burst is meant to fit one coalescing window (~30 ms of 50). On a loaded machine the sleeps stretch and the
	// burst can span several windows; each window may then cost a build, so the bound follows the span actually taken.
	span := time.Since(burstStart)
	require.Eventually(t, func() bool { return builds.Load() > before }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(4 * wsKickCoalesce) // let any second build show up
	got := builds.Load() - before
	maxBuilds := 1 + int64(span/wsKickCoalesce)
	require.LessOrEqual(t, got, maxBuilds, "a burst of kicks across two clients costs one build per coalescing window it spans (burst took %v)", span)
	if span < wsKickCoalesce {
		require.Equal(t, int64(1), got, "a burst that fits one window costs one build")
	}
}
