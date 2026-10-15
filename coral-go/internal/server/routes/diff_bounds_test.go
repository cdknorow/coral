package routes

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3558 (Security's #3556 finding 1): /diff?filepath= has file-content's bounds for anything
// but this computer's dashboard: a .env, a key, .git/config (its remote URL can carry a
// token) and a symlink out are refused as missing; an ordinary file's diff comes without the
// absolute working directory. The local dashboard is unchanged.
func TestDiffBounds(t *testing.T) {
	e := newBoundsEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(e.repo, ".git", "config.extra"), nil, 0o644))
	q := func(fp string) url.Values { return url.Values{"filepath": {fp}, "session_id": {"fb-1"}} }
	for _, k := range []kind{kindTunnel, kindCurl, kindBearer} {
		for _, fp := range []string{".env", "certs/server.pem", ".git/config", "app/linkfile"} {
			code, body := e.doAs("GET", "/api/sessions/live/claude-fb/diff", q(fp), "", k)
			assert.Equal(t, 404, code, "kind %d: %s: %s", k, fp, body)
			assert.NotContains(t, body, "TOKEN=abc")
			assert.NotContains(t, body, "-----BEGIN")
			assert.NotContains(t, body, "outside-secret")
		}
		code, body := e.doAs("GET", "/api/sessions/live/claude-fb/diff", q("README.md"), "", k)
		require.Equal(t, 200, code, body)
		var out map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Contains(t, out["diff"], "+hi")
		_, hasDir := out["working_directory"]
		assert.False(t, hasDir, "kind %d: no absolute path", k)
	}
	// The local dashboard: unchanged (it may read the .env diff, and gets the path).
	code, body := e.doAs("GET", "/api/sessions/live/claude-fb/diff", q(".env"), "", kindDashboard)
	require.Equal(t, 200, code, body)
	assert.Contains(t, body, "TOKEN=abc")
	assert.Contains(t, body, "working_directory")
}
