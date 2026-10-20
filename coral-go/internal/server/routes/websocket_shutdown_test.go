package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5366: a server shutdown ends the open dashboard sockets with 1001 (going away) and returns once their handlers have, and a socket that opens
// after it is refused; before, the handlers ran until the process exited, over a database that had closed.
func TestWSShutdown_CloseWebsocketsEndsOpenSocketsWithGoingAway(t *testing.T) {
	old := wsOpen
	wsOpen = &wsRegistry{conns: map[*websocket.Conn]struct{}{}}
	t.Cleanup(func() { wsOpen = old })

	server, _ := setupTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var conns []*websocket.Conn
	for i := 0; i < 3; i++ {
		c, _, err := websocket.Dial(ctx, "ws"+server.URL[4:]+"/ws/coral", nil)
		require.NoError(t, err)
		defer c.CloseNow()
		var first map[string]json.RawMessage
		require.NoError(t, wsjson.Read(ctx, c, &first))
		conns = append(conns, c)
	}
	// a client answers a close frame while it reads, as a browser does
	ends := make([]chan error, len(conns))
	for i, c := range conns {
		ends[i] = make(chan error, 1)
		go func() {
			for {
				if _, _, err := c.Read(ctx); err != nil {
					ends[i] <- err
					return
				}
			}
		}()
	}
	wsOpen.mu.Lock()
	assert.Len(t, wsOpen.conns, 3, "every open socket is tracked")
	wsOpen.mu.Unlock()

	start := time.Now()
	CloseWebsockets()
	assert.Less(t, time.Since(start), wsShutdownGrace, "the peers answered, so nothing waited out the grace")

	wsOpen.mu.Lock()
	assert.Empty(t, wsOpen.conns, "every handler has returned")
	wsOpen.mu.Unlock()
	for _, end := range ends {
		err := <-end
		assert.Equal(t, websocket.StatusGoingAway, websocket.CloseStatus(err), "the client is told the server is going away: %v", err)
	}

	// after the shutdown a new socket is accepted and closed again, never served
	c, resp, err := websocket.Dial(ctx, "ws"+server.URL[4:]+"/ws/coral", nil)
	if err == nil {
		defer c.CloseNow()
		_, _, err = c.Read(ctx)
		assert.Equal(t, websocket.StatusGoingAway, websocket.CloseStatus(err), "a socket opened after the shutdown is closed with going away: %v", err)
	} else if resp != nil {
		assert.NotEqual(t, http.StatusSwitchingProtocols, resp.StatusCode)
	}
}

// A peer that never answers the close frame does not hold the shutdown past the grace.
func TestWSShutdown_AnUnresponsivePeerIsDroppedAfterTheGrace(t *testing.T) {
	g := &wsRegistry{conns: map[*websocket.Conn]struct{}{}}
	accepted := make(chan *websocket.Conn, 1)
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		remove, ok := g.add(c)
		require.True(t, ok)
		defer remove()
		accepted <- c
		<-released // a handler that does not read: the close handshake cannot finish
		c.CloseNow()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+srv.URL[4:], nil)
	require.NoError(t, err)
	defer client.CloseNow()
	<-accepted

	grace := 200 * time.Millisecond
	start := time.Now()
	g.closeAll(grace)
	assert.Less(t, time.Since(start), 2*time.Second, "closeAll gave up on the peer and the handler after about two graces")
	assert.GreaterOrEqual(t, time.Since(start), grace, "it did wait for them first")
	close(released)
}

// closeAll returns when the handlers have returned, not when the close frames were sent: a handler that is slow to leave holds it (up to the grace), so the
// database is not closed under a handler that is still running.
func TestWSShutdown_CloseAllWaitsForTheHandlersToReturn(t *testing.T) {
	g := &wsRegistry{conns: map[*websocket.Conn]struct{}{}}
	const leaveAfter = 300 * time.Millisecond
	left := make(chan time.Time, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		remove, ok := g.add(c)
		require.True(t, ok)
		defer func() { left <- time.Now() }()
		defer remove()
		<-c.CloseRead(r.Context()).Done() // answers the close frame; the handler itself is slow to leave
		time.Sleep(leaveAfter)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+srv.URL[4:], nil)
	require.NoError(t, err)
	defer client.CloseNow()
	go func() {
		for {
			if _, _, err := client.Read(ctx); err != nil {
				return
			}
		}
	}()
	require.Eventually(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.conns) == 1 }, 5*time.Second, 10*time.Millisecond)

	g.closeAll(2 * time.Second)
	returned := time.Now()
	select {
	case at := <-left:
		assert.False(t, returned.Before(at), "closeAll returned before the handler had")
	default:
		t.Fatal("closeAll returned while its handler was still running")
	}
}
