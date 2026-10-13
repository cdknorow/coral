package hubproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

// markingTunnelResolver is tunnelResolver that also records MarkStatus calls.
type markingTunnelResolver struct {
	tunnelResolver
	mu    sync.Mutex
	marks []string
}

func (m *markingTunnelResolver) MarkStatus(_ context.Context, id, status, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marks = append(m.marks, id+":"+status)
}

func (m *markingTunnelResolver) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.marks...)
}

// A connector that refuses a path or query (an older machine that does not
// accept chat's before= yet, say) has answered; it is not down. The hub returns
// 403 path_not_allowed, which the client takes as "fall back to offset", and
// leaves the server's status alone, for HTTP and WebSocket alike. A real outage
// is still a 502 that marks the server unreachable.
func TestConnectorRefusalIs403AndDoesNotMarkUnreachable(t *testing.T) {
	local := http.NewServeMux()
	local.HandleFunc("/api/sessions/live/a/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"messages":[]}`))
	})
	sess := connectLocal(t, local, func(l *tunnel.LocalHandler) {
		l.AllowRequest = tunnel.AllowRequest(func() tunnel.Mode { return tunnel.TierLaunch })
	})
	res := &markingTunnelResolver{tunnelResolver: tunnelResolver{sess}}
	hub := cloudHub(t, res, nil)

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(hub.URL + "/api/remote/srv" + path)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	code, body := get("/api/sessions/live/a/chat?after=0&limit=200")
	require.Equal(t, http.StatusOK, code, body)

	// A query parameter the machine's route table does not list.
	code, body = get("/api/sessions/live/a/chat?after=0&limit=200&not_a_param=1")
	assert.Equal(t, http.StatusForbidden, code)
	assert.JSONEq(t, `{"error":"path not allowed","code":"path_not_allowed","server":"srv"}`, body)
	// A route the machine does not expose at all.
	code, _ = get("/api/system/api-key")
	assert.Equal(t, http.StatusForbidden, code)

	// The same refusal on a WebSocket handshake.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hub.URL, "http")+"/api/remote/srv/ws/not-a-feed",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {hub.URL}}})
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	assert.Empty(t, res.recorded(), "a refusal never changes the server's status")

	// An outage still reads as one.
	sess.Close()
	code, _ = get("/api/sessions/live/a/chat?after=0&limit=200")
	assert.Equal(t, http.StatusBadGateway, code)
	assert.Contains(t, res.recorded(), "srv:"+StatusUnreachable)
}
