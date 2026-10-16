package hubproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/redact"
	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingHandler notes each stream's hub user, then serves it as the connector does.
type recordingHandler struct {
	next  *tunnel.LocalHandler
	mu    sync.Mutex
	users []string
}

func (h *recordingHandler) ServeStream(ctx context.Context, s *tunnel.Stream) {
	h.mu.Lock()
	h.users = append(h.users, s.Header.HubUser)
	h.mu.Unlock()
	h.next.ServeStream(ctx, s)
}

func (h *recordingHandler) last() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.users) == 0 {
		return "<none>"
	}
	return h.users[len(h.users)-1]
}

// #3642: the hub sends the Coral Hub user with every tunnel stream (HTTP and WebSocket), for the
// connector's per-user limits, while X-Coral-Hub-User itself still reaches the local API only on
// a board write. A malformed or repeated value isn't carried.
func TestHubUserGoesWithEveryStream(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	local := http.NewServeMux()
	local.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = r.Header.Get(tunnel.HubUserHeader)
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			c, err := websocket.Accept(w, r, nil)
			if err == nil {
				c.Close(websocket.StatusNormalClosure, "")
			}
			return
		}
		w.Write([]byte("ok"))
	})
	ls := httptest.NewServer(local)
	defer ls.Close()

	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{})
		if err == nil {
			sessions <- s
		}
	}))
	defer ths.Close()
	rec := &recordingHandler{next: &tunnel.LocalHandler{Redactor: redact.Rules, BaseURL: ls.URL, AllowPath: allowAll}}
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler: rec, AllowInsecure: true,
	})
	require.NoError(t, err)
	defer cs.Close()
	sess := <-sessions
	defer sess.Close()
	hub := cloudHub(t, tunnelResolver{sess}, nil)

	const user = "hub-user-0123abcd"
	do := func(method, path string, values ...string) {
		t.Helper()
		req, _ := http.NewRequest(method, hub.URL+"/api/remote/srv"+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		for _, v := range values {
			req.Header.Add(tunnel.HubUserHeader, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode, path)
	}

	do("GET", "/api/sessions/live", user)
	assert.Equal(t, user, rec.last(), "a read carries the user to the connector")
	assert.Empty(t, seen["GET /api/sessions/live"], "but the local API never sees it on a read")

	do("POST", "/api/board/p/messages", user)
	assert.Equal(t, user, rec.last())
	assert.Equal(t, user, seen["POST /api/board/p/messages"], "a board write still forwards the header")

	do("GET", "/api/sessions/live", "Hub-User-0123ABCD")
	assert.Empty(t, rec.last(), "a malformed value isn't carried")
	do("GET", "/api/sessions/live", user, "hub-user-99999999")
	assert.Empty(t, rec.last(), "nor a repeated one")
	do("GET", "/api/sessions/live")
	assert.Empty(t, rec.last(), "nor a missing one")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hub.URL, "http")+"/api/remote/srv/ws/echo",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {hub.URL}, tunnel.HubUserHeader: {user}}})
	require.NoError(t, err)
	c.CloseNow()
	assert.Equal(t, user, rec.last(), "a WebSocket carries the user too")
	assert.Empty(t, seen["GET /ws/echo"])
}
