package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store/storetest"
)

// newNilBoardServer builds a server whose messageboard.db cannot be opened, so its board store is nil.
func newNilBoardServer(t *testing.T) *Server {
	t.Helper()
	cfg := newE2EConfig(t, t.TempDir())
	require.NoError(t, os.MkdirAll(cfg.CoralDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.CoralDir(), "messageboard.db"), []byte("not a database, and long enough to be read as one: 0123456789abcdef"), 0o600))
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	stack := newTermStack(t)
	srv, err := NewWithKey(cfg, db, stack.backend, stack.terminal, "")
	require.NoError(t, err)
	require.Nil(t, srv.boardStore, "the board could not be opened")
	t.Cleanup(srv.Close)
	return srv
}

// #4888: a board database that can't be opened is logged and the server runs without the board, as
// the warning says; the constructor went on to wipe the board's webhook URLs through the nil store and
// panicked (cmd/coral TestServer_RootServesHTML, once in a whole-module run on epoch).
func TestNewWithKeyRunsWithoutABoardItCannotOpen(t *testing.T) {
	srv := newNilBoardServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the dashboard is still served")
}

// #4924: with a nil board store, every board use reachable after the constructor is guarded. The
// background goroutines (started from the router via s.goBackground, whose safego.Recover swallows a
// panic -- which hid the RestoreSubscriberReminders nil deref from #4888's test) do not panic, and the
// board HTTP routes answer 503 "board unavailable" rather than dereferencing the nil store.
func TestNilBoardStoreIsGuardedAcrossTheServer(t *testing.T) {
	srv := newNilBoardServer(t)
	ts := httptest.NewServer(srv.Router()) // builds the routes and launches the background goroutines
	t.Cleanup(ts.Close)
	require.NotNil(t, srv.boardHandler, "the router set the board handler")

	// The background restores, called directly so a nil-board panic is observed rather than swallowed.
	require.NotPanics(t, func() {
		assert.NoError(t, srv.boardHandler.RestoreSubscriberReminders(context.Background()))
		srv.boardHandler.RecoverTaskNotifications(context.Background())
	})

	key := srv.keyStore.Key()
	get := func(method, path string) (int, string) {
		req, err := http.NewRequest(method, ts.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body)
	}
	for _, path := range []string{"/api/board/projects", "/api/board/tasks", "/api/board/team/messages"} {
		code, body := get("GET", path)
		assert.Equal(t, http.StatusServiceUnavailable, code, "GET %s", path)
		assert.Contains(t, body, "board unavailable", "GET %s", path)
	}
	// A route that does not need the board is not swept into the 503 (it uses the artifact store).
	code, _ := get("GET", "/api/session-artifacts")
	assert.NotEqual(t, http.StatusServiceUnavailable, code, "session artifacts do not need the board")
}
