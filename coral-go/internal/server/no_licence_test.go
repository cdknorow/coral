package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store/storetest"
)

// Coral has no licence (#3221): no licence API, no supporter reminder instead of the
// dashboard, and no store or licence fields in the system status. The EULA is separate
// and unchanged.
func newNoLicenceServer(t *testing.T, prepare func(coralDir string)) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	if prepare != nil {
		prepare(cfg.CoralDir())
	}
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	ts := httptest.NewServer(New(cfg, db, stack.backend, stack.terminal).Router())
	t.Cleanup(ts.Close)
	return ts
}

func TestNoLicenceAPI(t *testing.T) {
	ts := newNoLicenceServer(t, nil)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/license/status"},
		{"POST", "/api/license/activate"},
		{"POST", "/api/license/deactivate"},
		{"POST", "/api/license/webhook"},
	} {
		req, _ := http.NewRequest(c.method, ts.URL+c.path, strings.NewReader(`{"license_key":"X"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		assert.True(t, resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed,
			"%s %s: want no route, got %d %s", c.method, c.path, resp.StatusCode, body)
	}
}

// The old reminder replaced the dashboard on every 25th launch after Coral first delivered
// a result. Even with that launch state (and an old licence file) on disk, every load is
// the dashboard.
func TestNoSupporterReminderOnAnyLaunch(t *testing.T) {
	ts := newNoLicenceServer(t, func(coralDir string) {
		require.NoError(t, os.MkdirAll(coralDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(coralDir, ".launch-count"), []byte("24"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(coralDir, ".launch-anchor"), []byte("0"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(coralDir, "license.dat"), []byte("old"), 0o600))
	})
	for i := 0; i < 3; i++ {
		resp, err := http.Get(ts.URL + "/")
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		page := string(body)
		assert.Contains(t, page, `id="live-sessions-list"`, "load %d is the dashboard", i)
		for _, nag := range []string{"Buy a License", "Continue Free", "Supporter License", "store.coralai.ai"} {
			assert.NotContains(t, page, nag, "load %d", i)
		}
	}
}

func TestSystemStatusHasNoStoreOrLicenceFields(t *testing.T) {
	ts := newNoLicenceServer(t, nil)
	resp, err := http.Get(ts.URL + "/api/system/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	for _, k := range []string{"store_url", "supporter_urls", "skip_license"} {
		assert.NotContains(t, body, k)
	}
	assert.Contains(t, body, "tier_name")
}
