//go:build windows

package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #59 (QA #28 C3): a file:///C:/... link an agent printed opens on Windows, with a line range,
// through localhost, and with a percent-encoded space in its folder.
func TestResolvePathTakesWindowsFileURLs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	server, _, _, ss := setupSessionsTestServer(t)
	root := filepath.Join(t.TempDir(), "my repo")
	app := filepath.Join(root, "app dir")
	require.NoError(t, os.MkdirAll(app, 0o755))
	require.NoError(t, exec.Command("git", "init", "-q", root).Run())
	main := filepath.Join(app, "main.go")
	require.NoError(t, os.WriteFile(main, []byte("package main"), 0o644))
	ss.RegisterLiveSession(context.Background(), &store.LiveSession{AgentName: "claude-fileurl", AgentType: "claude", WorkingDir: app, SessionID: "fileurl-1"})

	resolve := func(ref string) (int, map[string]any) {
		q := url.Values{"filepath": {ref}, "session_id": {"fileurl-1"}}
		req, _ := http.NewRequest("GET", server.URL+"/api/sessions/live/claude-fileurl/resolve-path?"+q.Encode(), nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	slashed := filepath.ToSlash(main) // C:/Users/.../my repo/app dir/main.go
	encoded := strings.ReplaceAll(slashed, " ", "%20")
	for ref, line := range map[string]float64{
		"file:///" + slashed:                                    0,
		"file:///" + encoded:                                    0,
		"file:///" + encoded + "#L12-L30":                       12,
		"file://localhost/" + encoded:                           0,
		"FILE://LOCALHOST/" + slashed + "#L7":                   7,
		"file:///" + strings.ToLower(slashed[:1]) + slashed[1:]: 0,
	} {
		code, body := resolve(ref)
		assert.Equal(t, http.StatusOK, code, ref)
		assert.Equal(t, "app dir/main.go", body["filepath"], ref)
		if line > 0 {
			assert.Equal(t, line, body["line"], ref)
		}
	}
	// A UNC link is a path like any other: refused here, outside the repository and not local.
	code, _ := resolve("file://no-such-server/share/main.go")
	assert.Equal(t, http.StatusNotFound, code)
}
