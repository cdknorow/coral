package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #3515 end to end, the real router: an agent's own post (its token) that links a fresh
// image in its working directory is stored with the link pointed at the new artifact, which
// the artifact route then serves; the same post from the Operator, or from a token-less
// script, is stored as written; and the Operator's switch turns it off. #3563: it is off until
// the Operator turns it on, so a Coral that never set it stores the link as written.
func TestBoardAutoAttachEndToEnd(t *testing.T) {
	srv, ts, _ := strictServer(t, false)
	ctx := context.Background()
	work, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	var img bytes.Buffer
	require.NoError(t, png.Encode(&img, image.NewGray(image.Rect(0, 0, 3, 3))))
	require.NoError(t, os.WriteFile(filepath.Join(work, "shot.png"), img.Bytes(), 0o644))

	sid := "dddddddd-3515-0000-0000-00000000000d"
	ss := store.NewSessionStore(srv.db)
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "dev", WorkingDir: work}))
	tok, err := srv.db.MintSessionToken(ctx, sid, "claude-"+sid)
	require.NoError(t, err)
	_, err = srv.boardStore.Subscribe(ctx, "team", "Dev", "Dev", "claude-"+sid, nil, "all")
	require.NoError(t, err)

	do := func(method, path, body string, hdr http.Header) (int, []byte) {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	post := func(content string, hdr http.Header, as string) string {
		body, _ := json.Marshal(map[string]string{"subscriber_id": as, "content": content})
		code, b := do("POST", "/api/board/team/messages", string(body), hdr)
		require.Equal(t, http.StatusOK, code, string(b))
		var out map[string]any
		require.NoError(t, json.Unmarshal(b, &out))
		return out["content"].(string)
	}
	asAgent := http.Header{store.SessionTokenHeader: {tok}}
	link := regexp.MustCompile(`^done: !\[run\]\(coral://artifacts/([0-9a-f]{64})\)$`)

	// Never set: off (#3563). The link is stored as written and nothing is uploaded.
	settings, err := ss.GetSettings(ctx)
	require.NoError(t, err)
	_, set := settings["board_auto_attach"]
	require.False(t, set, "a fresh Coral has no board_auto_attach setting")
	assert.Equal(t, "done: ![run](shot.png)", post("done: ![run](shot.png)", asAgent, "Dev"), "off by default")
	objects, _ := filepath.Glob(filepath.Join(srv.cfg.CoralDir(), "artifacts", "*", "*"))
	assert.Empty(t, objects, "nothing was stored")

	// The Operator turns it on, as the dashboard's Settings does.
	code, b := do("PUT", "/api/settings", `{"board_auto_attach":true}`, nil)
	require.Equal(t, http.StatusOK, code, string(b))

	stored := post("done: ![run](shot.png)", asAgent, "Dev")
	m := link.FindStringSubmatch(stored)
	require.NotNil(t, m, "the agent's link points at the artifact: %q", stored)
	code, b = do("GET", "/api/artifacts/"+m[1]+"?v=2", "", nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, img.Bytes(), b, "the artifact is the image")

	// The board keeps the rewritten text.
	code, b = do("GET", "/api/board/team/messages/all", "", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(b), "coral://artifacts/"+m[1])

	// The Operator's post, and a token-less script's, are stored as written.
	assert.Equal(t, "done: ![run](shot.png)", post("done: ![run](shot.png)", nil, "Operator"))
	assert.Equal(t, "done: ![run]("+filepath.Join(work, "shot.png")+")", post("done: ![run]("+filepath.Join(work, "shot.png")+")", nil, "Dev"))

	// The Operator's switch.
	code, b = do("PUT", "/api/settings", `{"board_auto_attach":"false"}`, nil)
	require.Equal(t, http.StatusOK, code, string(b))
	assert.Equal(t, "done: ![run](shot.png)", post("done: ![run](shot.png)", asAgent, "Dev"), "switched off")
	code, b = do("PUT", "/api/settings", `{"board_auto_attach":true}`, nil)
	require.Equal(t, http.StatusOK, code, string(b))
	assert.Regexp(t, link, post("done: ![run](shot.png)", asAgent, "Dev"), "back on")
	code, b = do("PUT", "/api/settings", `{"board_auto_attach":false}`, nil)
	require.Equal(t, http.StatusOK, code, string(b))
	assert.Equal(t, "done: ![run](shot.png)", post("done: ![run](shot.png)", asAgent, "Dev"), "a JSON false (stored as False) is off too")
}

// #3515, #3623, #3645: the dashboard and the agent pop-out send the CSP (scripts, media and images
// from this server only and no inline script or handler, forms post only here, no <base> or
// plugins, framed only by Coral itself).
func TestDashboardPagesSendTheCSP(t *testing.T) {
	_, ts, _ := strictServer(t, false)
	for _, path := range []string{"/", "/agent/eeeeeeee-3515-0000-0000-00000000000e"} {
		resp, err := http.Get(ts.URL + path)
		require.NoError(t, err)
		resp.Body.Close()
		csp := resp.Header.Get("Content-Security-Policy")
		for _, d := range []string{"script-src 'self';", "form-action 'self'", "media-src 'self' blob: data:",
			"img-src 'self' data: blob:", "base-uri 'self'", "object-src 'none'", "frame-ancestors 'self'"} {
			assert.Contains(t, csp, d, path)
		}
		// No inline script or handler runs on the dashboard (#3645 stage 2).
		assert.NotContains(t, csp, "unsafe-inline", path)
		assert.NotContains(t, csp, "unsafe-eval", path)
	}
}
