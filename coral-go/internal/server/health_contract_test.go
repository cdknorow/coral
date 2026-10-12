package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store/storetest"
)

// GET /api/health is a contract: the native app polls it to detect crashes,
// and hubs and the Cloud Hub use it as their liveness check while a /ws/coral
// feed carries the session list. Changing its shape must be deliberate.
func TestHealthEndpointContract(t *testing.T) {
	for _, hub := range []bool{false, true} {
		dir := t.TempDir()
		cfg := newE2EConfig(t, dir)
		cfg.HubMode = hub
		db, err := storetest.Open(t, cfg.DBPath)
		require.NoError(t, err)
		t.Cleanup(func() { db.Close() })
		stack := newTermStack(t)
		srv := New(cfg, db, stack.backend, stack.terminal)

		ts := httptest.NewServer(srv.Router())
		t.Cleanup(ts.Close)
		resp, err := http.Get(ts.URL + "/api/health")
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		assert.Equal(t, []string{"hub", "status", "version"}, keys, "exact field set")
		assert.Equal(t, "ok", body["status"])
		assert.IsType(t, "", body["version"])
		assert.Equal(t, hub, body["hub"])
	}
}
