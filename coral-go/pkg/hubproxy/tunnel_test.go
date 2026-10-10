package hubproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

type tunnelResolver struct{ sess *tunnel.Session }

func (r tunnelResolver) Resolve(_ context.Context, id string) (Target, error) {
	if id != "srv" {
		return nil, ErrUnknownServer
	}
	return TunnelTarget{Session: r.sess}, nil
}

// TestProxyOverTunnel runs the real handler through a real tunnel to a
// LocalHandler in front of a fake Coral API: HTTP, SSE and WebSocket.
func TestProxyOverTunnel(t *testing.T) {
	local := http.NewServeMux()
	local.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s auth=%q cookie=%q", r.URL.Query().Get("n"), r.Header.Get("Authorization"), r.Header.Get("Cookie"))
	})
	local.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: 1\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	local.HandleFunc("/ws/echo", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			c.Write(r.Context(), typ, b)
		}
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
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler: &tunnel.LocalHandler{BaseURL: ls.URL}, AllowInsecure: true,
	})
	require.NoError(t, err)
	defer cs.Close()
	sess := <-sessions
	defer sess.Close()

	r := chi.NewRouter()
	r.Handle("/api/remote/{server}/*", New(tunnelResolver{sess}))
	hub := httptest.NewServer(r)
	defer hub.Close()

	// HTTP: hub-side credentials never reach the local API.
	req, _ := http.NewRequest("GET", hub.URL+"/api/remote/srv/api/hello?n=bob", nil)
	req.Header.Set("Authorization", "Bearer browser-session")
	req.Header.Set("Cookie", "sid=1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, `hello bob auth="" cookie=""`, string(b))

	// Unknown server and disallowed paths are rejected by the handler.
	resp, _ = http.Get(hub.URL + "/api/remote/nope/api/hello")
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = http.Get(hub.URL + "/api/remote/srv/admin/x")
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// SSE: the first event arrives while the stream is still open.
	req, _ = http.NewRequest("GET", hub.URL+"/api/remote/srv/api/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: 1\n", line)
	resp.Body.Close()

	// WebSocket bridge.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hub.URL, "http")+"/api/remote/srv/ws/echo", nil)
	require.NoError(t, err)
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte("ping")))
	_, msg, err := c.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(msg))
	c.Close(websocket.StatusCode(4002), "bye")
	require.Eventually(t, func() bool { return sess.NumStreams() == 0 }, 3*time.Second, 20*time.Millisecond)
}
