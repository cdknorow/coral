package background

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	proxy "github.com/cdknorow/coral/pkg/hubproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The liveness poll and the hub's proxied calls share one connection pool, so
// a remote that is being polled keeps a warm connection for the next chat
// switch: health checks and a following /chat fetch all ride one TCP
// connection. A poll that closed its response early would drop the connection
// every time and leave every chat switch to dial (and TLS-handshake) afresh.
func TestHub_HealthPollKeepsTheProxyConnectionWarm(t *testing.T) {
	var dials atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/health":
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": "fake-1"})
		default:
			json.NewEncoder(w).Encode(map[string]any{"messages": []any{}, "start": 0})
		}
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			dials.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	reg := newFakeRegistry()
	reg.put("r1", srv.URL)
	h := NewRemoteAgentHub(reg, regResolver{reg}, RemoteAgentHubConfig{RequestTimeout: 2 * time.Second})
	ctx := context.Background()
	for range 3 {
		require.NoError(t, h.checkHealth(ctx, "r1"))
	}
	h.refreshVersion(ctx, "r1")

	target, err := regResolver{reg}.Resolve(ctx, "r1")
	require.NoError(t, err)
	resp, err := proxy.Get(ctx, target, "/api/sessions/live/a/chat", "after=0")
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	assert.Equal(t, int32(1), dials.Load(), "health checks and the chat fetch share one pooled connection")
}
