package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4888: a board database that can't be opened is logged and the server runs without the board, as
// the warning says; the constructor went on to wipe the board's webhook URLs through the nil store and
// panicked (cmd/coral TestServer_RootServesHTML, once in a whole-module run on epoch).
func TestNewWithKeyRunsWithoutABoardItCannotOpen(t *testing.T) {
	cfg := newE2EConfig(t, t.TempDir())
	require.NoError(t, os.MkdirAll(cfg.CoralDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.CoralDir(), "messageboard.db"), []byte("not a database, and long enough to be read as one: 0123456789abcdef"), 0o600))
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	stack := newTermStack(t)

	var srv *Server
	require.NotPanics(t, func() { srv, err = NewWithKey(cfg, db, stack.backend, stack.terminal, "") })
	require.NoError(t, err)
	require.NotNil(t, srv)
	assert.Nil(t, srv.boardStore, "the board could not be opened")
	t.Cleanup(srv.Close)

	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the dashboard is still served")
}
