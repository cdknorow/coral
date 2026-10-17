package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #3966 (c) through the real router, strict local auth on: "sign out other browsers" closes the
// other browser's open live-feed WebSocket, and leaves this browser's open.
func TestSignOutOthersClosesTheirWebSockets(t *testing.T) {
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	cfg.StrictLocalAuth = true
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := mustServer(NewWithKey(cfg, db, stack.backend, stack.terminal, ""))
	t.Cleanup(srv.Close) // its loops and the board database, which Windows won't remove while open (#3914)
	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)

	cookie := func() *http.Cookie {
		r := httptest.NewRequest("GET", local.URL+"/", nil)
		tok, err := srv.keyStore.NewSession(r, auth.SessionByPassword, false)
		require.NoError(t, err)
		return &http.Cookie{Name: srv.keyStore.SessionCookieName(r), Value: tok}
	}
	me, other := cookie(), cookie()
	dial := func(c *http.Cookie) *websocket.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hdr := http.Header{"Origin": {local.URL}, "Cookie": {c.String()}}
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(local.URL, "http")+"/ws/coral", &websocket.DialOptions{HTTPHeader: hdr})
		require.NoError(t, err)
		t.Cleanup(func() { conn.CloseNow() })
		return conn
	}
	mine, theirs := dial(me), dial(other)
	// readUntilClosed reads frames until the socket closes (true) or the wait ends (false).
	readUntilClosed := func(c *websocket.Conn, wait time.Duration) bool {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return ctx.Err() == nil
			}
		}
	}

	req, _ := http.NewRequest("POST", local.URL+"/api/auth/sessions/sign-out-others", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", local.URL)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(me)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.True(t, readUntilClosed(theirs, 10*time.Second), "the other browser's socket closed")
	assert.False(t, readUntilClosed(mine, 500*time.Millisecond), "this browser's socket stays open")
}
