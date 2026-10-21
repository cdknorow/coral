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
	"time"

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
	// A network path is refused by its text, at once, before anything touches the filesystem
	// (resolving it would make Windows connect to the host and send the user's credentials):
	// a file: URL with a host, a \\server\share path and its forward-slash form (PR #44 review).
	for _, ref := range []string{"file://unreachable.invalid/s/x.go", `\\unreachable.invalid\s\x.go`, "//unreachable.invalid/s/x.go"} {
		start := time.Now()
		code, _ := resolve(ref)
		assert.Equal(t, http.StatusNotFound, code, ref)
		assert.Less(t, time.Since(start), time.Second, "%s: refused without a network attempt", ref)
	}
}
