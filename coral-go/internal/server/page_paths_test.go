package server

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One sample URL per dashboard page family (#3532); every family in DashboardPagePaths must
// have one, so a family added without a test fails here.
var pageSamples = map[string]string{
	"/agents":       "/agents",
	"/agents/*":     "/agents/@m2/6f9a3c1e-0000-4000-8000-000000000001/files",
	"/chats":        "/chats?q=battle&page=2",
	"/chats/{id}":   "/chats/42",
	"/team/*":       "/team/@m2/coral-go-team/settings",
	"/board/*":      "/board/%40odd",
	"/workflows":    "/workflows",
	"/workflows/*":  "/workflows/7/runs/3",
	"/jobs":         "/jobs",
	"/analytics":    "/analytics",
	"/docs":         "/docs",
	"/docs/{name}":  "/docs/board",
	"/servers":      "/servers",
	"/servers/{id}": "/servers/m2",
}

func pageGet(t *testing.T, c *http.Client, method, u string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, u, nil)
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// Each page path serves exactly the shell "/" serves (GET only, same headers and ETag,
// Referrer-Policy same-origin), and nothing else changed: misses under /api, /ws, /auth and
// /static, and unknown paths, stay 404 and not HTML (no catch-all).
func TestDashboardPagePathsServeTheShell(t *testing.T) {
	for _, p := range DashboardPagePaths {
		_, ok := pageSamples[p]
		require.True(t, ok, "no sample URL for page family %s", p)
	}
	_, ts, _ := strictServer(t, false)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	root, rootBody := pageGet(t, c, "GET", ts.URL+"/")
	require.Equal(t, 200, root.StatusCode)
	rootPost, _ := pageGet(t, c, "POST", ts.URL+"/")
	assert.Equal(t, "same-origin", root.Header.Get("Referrer-Policy"), "the shell sends Referrer-Policy")
	for fam, sample := range pageSamples {
		resp, body := pageGet(t, c, "GET", ts.URL+sample)
		assert.Equal(t, 200, resp.StatusCode, "%s (%s)", sample, fam)
		assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html"), sample)
		assert.Equal(t, rootBody, body, "%s: the same shell as /", sample)
		assert.Equal(t, root.Header.Get("ETag"), resp.Header.Get("ETag"), "%s: the same ETag", sample)
		for _, h := range []string{"Referrer-Policy", "X-Frame-Options", "X-Content-Type-Options", "Content-Security-Policy"} {
			assert.Equal(t, root.Header.Get(h), resp.Header.Get(h), "%s: header %s as on /", sample, h)
		}
		post, _ := pageGet(t, c, "POST", ts.URL+sample)
		assert.Equal(t, rootPost.StatusCode, post.StatusCode, "%s: POST answers as on /", sample)
	}
	for _, miss := range []string{"/api/nope-3532", "/ws/nope", "/auth/nope", "/static/missing-3532.js", "/random-3532", "/agentsx", "/chat/x", "/history"} {
		resp, _ := pageGet(t, c, "GET", ts.URL+miss)
		assert.False(t, resp.StatusCode == 200 && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html"), "%s must not get the shell (%d %s)", miss, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

// With strict local auth, a signed-out page path is refused like "/" and the refusal keeps the
// path (and query) in return_to, so sign-in lands on the same view.
func TestDashboardPagePathsStrictKeepTheView(t *testing.T) {
	_, ts, _ := strictServer(t, true)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	root, _ := pageGet(t, c, "GET", ts.URL+"/")
	for _, sample := range []string{"/team/@m2/coral-go-team/settings", "/chats?q=battle&page=2", "/agents/@m2/6f9a3c1e-0000-4000-8000-000000000001"} {
		resp, _ := pageGet(t, c, "GET", ts.URL+sample)
		assert.Equal(t, root.StatusCode, resp.StatusCode, "%s: refused like /", sample)
		loc, err := url.Parse(resp.Header.Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, sample, loc.Query().Get("return_to"), "%s: return_to keeps the view", sample)
	}
}
