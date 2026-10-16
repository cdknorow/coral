package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store/storetest"
)

// #3262 B: the dashboard's router never serves Go's profiles, whatever the address it
// listens on (pprof has its own opt-in loopback listener, CORAL_PPROF_ADDR).
func TestMainRouterDoesNotServePprof(t *testing.T) {
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	ts := httptest.NewServer(NewWithKey(cfg, db, stack.backend, stack.terminal, "").Router())
	t.Cleanup(ts.Close)
	for _, p := range []string{"/debug/pprof/", "/debug/pprof/goroutine?debug=1", "/debug/pprof/heap", "/debug/pprof/cmdline", "/api/debug/pprof/"} {
		resp, err := http.Get(ts.URL + p)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body := string(b)
		assert.False(t, strings.Contains(body, "goroutine profile") || strings.Contains(body, "Types of profiles available") || strings.Contains(body, "heap profile"), p)
	}
}
