package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/store"
)

// flakyRemote answers the hub's own probes, but drops the connection of any
// request to /api/system/status while dropping is set: a blip that only a
// proxied dashboard request runs into.
func flakyRemote(t *testing.T, key string, dropping *atomic.Bool) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/health":
			w.Write([]byte(`{"status":"ok","version":"9.9.9"}`))
		case "/api/sessions/live":
			w.Write([]byte(`[]`))
		case "/api/system/status":
			if dropping.Load() {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (e *rsEnv) rowStatus(t *testing.T, id string) string {
	t.Helper()
	w := e.do("GET", "/api/servers", nil)
	require.Equal(t, 200, w.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	for _, r := range rows {
		if r["id"] == id {
			s, _ := r["status"].(string)
			return s
		}
	}
	return ""
}

// QA's #3202 repro through the real proxy, registry and remote worker: one
// proxied request fails during a blip while the remote is otherwise fine. The
// Servers page row (GET /api/servers) used to stay 'unreachable' until the
// worker's once-a-minute write or a Test click; the failure now goes through
// the worker, which re-probes at once.
func TestRemoteStatus_ProxyBlipRecoversWithoutTest(t *testing.T) {
	e := newRSEnv(t)
	var dropping atomic.Bool
	remote := flakyRemote(t, routeSecret, &dropping)
	w := e.do("POST", "/api/servers", map[string]any{
		"id": "boxb", "label": "Box B", "url": remote.URL, "api_key": routeSecret, "allow_private": true})
	require.Equal(t, 201, w.Code, w.Body.String())

	resolver := NewRemoteResolver(e.h.Store())
	const pollEvery = 5 * time.Second
	hub := background.NewRemoteAgentHub(e.h.Store(), resolver, background.RemoteAgentHubConfig{
		PollInterval: pollEvery, RequestTimeout: time.Second,
		RegistryRefresh: 50 * time.Millisecond, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		OfflineGrace: -1, // each failure's status at once (#3649 tests the grace in background)
	})
	resolver.SetFailureSink(hub)
	MountRemoteProxy(e.router, resolver)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	require.Eventually(t, func() bool { return e.rowStatus(t, "boxb") == store.RemoteStatusOnline }, 3*time.Second, 20*time.Millisecond)

	dropping.Store(true)
	start := time.Now()
	w = e.do("GET", "/api/remote/boxb/api/system/status", nil)
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Equal(t, store.RemoteStatusUnreachable, e.rowStatus(t, "boxb"), "the proxied failure is recorded at once")
	dropping.Store(false)

	require.Eventually(t, func() bool { return e.rowStatus(t, "boxb") == store.RemoteStatusOnline }, 3*time.Second, 20*time.Millisecond,
		"the row is online again without a Test click")
	assert.Less(t, time.Since(start), pollEvery, "recovered by the immediate re-probe, not the poll schedule or the minute write")
	statuses := map[string]string{}
	for _, s := range hub.Snapshot().Servers {
		statuses[s.ID] = s.Status
	}
	assert.Equal(t, store.RemoteStatusOnline, statuses["boxb"], "the worker agrees with the row")
}

// Without a remote worker (no hub sink, or a server the hub does not run) the
// proxy still records the failure in the row, as before.
func TestRemoteStatus_MarkStatusWithoutWorkerWritesRow(t *testing.T) {
	e := newRSEnv(t)
	remote := fakeRemote(t, routeSecret)
	w := e.do("POST", "/api/servers", map[string]any{
		"id": "boxc", "label": "Box C", "url": remote.URL, "api_key": routeSecret, "allow_private": true})
	require.Equal(t, 201, w.Code, w.Body.String())

	resolver := NewRemoteResolver(e.h.Store())
	resolver.MarkStatus(context.Background(), "boxc", store.RemoteStatusUnreachable, "connect failed")
	assert.Equal(t, store.RemoteStatusUnreachable, e.rowStatus(t, "boxc"))

	// A hub that is not running a worker for the server declines, and the row is written.
	hub := background.NewRemoteAgentHub(e.h.Store(), resolver, background.RemoteAgentHubConfig{})
	resolver.SetFailureSink(hub)
	resolver.MarkStatus(context.Background(), "boxc", store.RemoteStatusUnauthorized, "remote rejected API key")
	assert.Equal(t, store.RemoteStatusUnauthorized, e.rowStatus(t, "boxc"))
}
