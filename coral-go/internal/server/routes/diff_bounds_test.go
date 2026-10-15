package routes

import (
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
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
	// Tracked and modified, so a pathspec that matched more than one file would show them.
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", e.repo, "-c", "user.email=t@example.test", "-c", "user.name=t"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("add", "README.md", ".env", "certs/server.pem", "app/main.go")
	git("commit", "-q", "-m", "c")
	for p, s := range map[string]string{"README.md": "hi there", ".env": "TOKEN=abcd", "certs/server.pem": "-----BEGIN-----\nX", "app/main.go": "package main\n// edited"} {
		require.NoError(t, os.WriteFile(filepath.Join(e.repo, p), []byte(s), 0o644))
	}
	q := func(fp string) url.Values { return url.Values{"filepath": {fp}, "session_id": {"fb-1"}} }
	for _, k := range []kind{kindTunnel, kindCurl, kindBearer} {
		for _, fp := range []string{".env", "certs/server.pem", ".git/config", "app/linkfile"} {
			code, body := e.doAs("GET", "/api/sessions/live/claude-fb/diff", q(fp), "", k)
			assert.Equal(t, 404, code, "kind %d: %s: %s", k, fp, body)
			assert.NotContains(t, body, "TOKEN=abc")
			assert.NotContains(t, body, "-----BEGIN")
			assert.NotContains(t, body, "outside-secret")
		}
		// A filepath is one file: pathspec magic matches nothing, a folder is refused.
		for _, fp := range []string{"*", ":(glob)**", ":/", "certs", "app"} {
			code, body := e.doAs("GET", "/api/sessions/live/claude-fb/diff", q(fp), "", k)
			assert.Contains(t, []int{200, 404}, code, "kind %d: %s: %s", k, fp, body)
			assert.NotContains(t, body, "TOKEN=abc", "kind %d: %s", k, fp)
			assert.NotContains(t, body, "-----BEGIN", "kind %d: %s", k, fp)
			assert.NotContains(t, body, "+hi", "kind %d: %s: only README.md's own diff shows it", k, fp)
			assert.NotContains(t, body, "edited", "kind %d: %s: only app/main.go's own diff shows it", k, fp)
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
