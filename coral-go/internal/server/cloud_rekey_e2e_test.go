//go:build !windows

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/secretbox"
)

// #3698, the real router: when the secret key is refused, /api/cloud/status says so and what a
// re-key discards; the re-key needs {"confirm": true}, then replaces the key and discards the
// Hub enrollment and the remote servers' API keys; a usable key gets 409. (Who may call it: the
// cloud gate test and the Operator-only table test.)
func TestRekeyThroughTheDashboard(t *testing.T) {
	t.Setenv(secretbox.EnvKey, "")
	r := newE2ERemote(t, "rekey", nil)
	h := r.srv.Router()
	ctx := context.Background()
	dir := r.cfg.CoralDir()
	rs := store.NewRemoteServerStore(r.db, dir)
	_, err := rs.Add(ctx, "r1", "Remote", "https://r1.example.com", "OLD-API-KEY", false)
	require.NoError(t, err)
	require.NoError(t, store.NewCloudStore(r.db, rs).Save(ctx, "https://hub.example.com", "srv_1", "Laptop", "OLD-HUB-TOKEN"))

	do := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr, req.Host = "127.0.0.1:50000", "127.0.0.1:8420"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Origin", "http://127.0.0.1:8420")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, st := do("GET", "/api/cloud/status", "")
	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, st["secret_key"], "a usable key: nothing to offer")
	code, _ = do("POST", "/api/cloud/rekey", `{"confirm":true}`)
	assert.Equal(t, http.StatusConflict, code, "a usable key is never replaced")

	require.NoError(t, os.Chmod(secretbox.KeyFilePath(dir), 0o644))
	code, st = do("GET", "/api/cloud/status", "")
	require.Equal(t, http.StatusOK, code)
	sk, _ := st["secret_key"].(map[string]any)
	require.NotNil(t, sk, "the refused key is reported")
	assert.Equal(t, "refused", sk["state"])
	assert.EqualValues(t, 1, sk["remote_server_keys"])
	assert.Equal(t, "https://hub.example.com", sk["hub_url"])

	code, _ = do("POST", "/api/cloud/rekey", `{}`)
	assert.Equal(t, http.StatusBadRequest, code, "not without confirm")
	_, err = secretbox.LoadKey(dir)
	require.Error(t, err, "nothing changed")

	code, out := do("POST", "/api/cloud/rekey", `{"confirm":true}`)
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.EqualValues(t, 1, out["discarded_remote_server_keys"])
	assert.Equal(t, true, out["discarded_hub_enrollment"])
	_, err = secretbox.LoadKey(dir)
	require.NoError(t, err, "works after")
	code, st = do("GET", "/api/cloud/status", "")
	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, st["secret_key"])
	var apiKey, hub string
	require.NoError(t, r.db.GetContext(ctx, &apiKey, "SELECT api_key FROM remote_servers WHERE id = 'r1'"))
	assert.Empty(t, apiKey, "the old API key is gone")
	assert.Error(t, r.db.GetContext(ctx, &hub, "SELECT hub_url FROM cloud_connection"), "the Hub enrollment is gone")

	code, _ = do("POST", "/api/cloud/rekey", `{"confirm":true}`)
	assert.Equal(t, http.StatusConflict, code, "once is enough")
}
