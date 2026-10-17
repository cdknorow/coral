package server

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/frontend"
)

var preloadRe = regexp.MustCompile(`<link rel="modulepreload" href="([^"]+)">`)

func hintsOf(body string) map[string]bool {
	got := map[string]bool{}
	for _, m := range preloadRe.FindAllStringSubmatch(body, -1) {
		got[m[1]] = true
	}
	return got
}

func assertHintsEveryStartupModule(t *testing.T, p, body string) {
	t.Helper()
	want := frontend.StartupModules()
	require.Greater(t, len(want), 100, "the startup graph is the real one")
	got := hintsOf(body)
	assert.Len(t, got, len(want), "%s: one hint per startup module", p)
	for _, m := range want {
		found := false
		for h := range got {
			if strings.HasSuffix(h, "/"+m) && strings.HasPrefix(h, "/static/b/") {
				found = true
				break
			}
		}
		assert.True(t, found, "%s: a hint for %s under /static/b/", p, m)
	}
	// Hints come after the stylesheets, so the render-blocking CSS is asked for first.
	assert.Less(t, strings.LastIndex(body, `rel="stylesheet"`), strings.Index(body, `rel="modulepreload"`), "%s: stylesheets before the module hints", p)
}

// The dashboard page is the startup bundle: one script, one stylesheet, under /static/bb/<build>/, and no per-module hints (#4238). The agent pop-out
// stays on the original files and hints the whole startup graph (#4131).
func TestShellIsTheBundleAndThePopoutHintsItsModules(t *testing.T) {
	_, ts, _ := strictServer(t, false)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, body := pageGet(t, c, "GET", ts.URL+"/")
	require.Equal(t, 200, resp.StatusCode)
	assert.Empty(t, hintsOf(body), "the bundled page has no per-module hints")
	assert.Regexp(t, `<script type="module" src="/static/bb/[0-9a-f]+/app\.js">`, body)
	assert.Equal(t, 1, strings.Count(body, `rel="stylesheet"`), "one stylesheet")
	assert.Regexp(t, `<link rel="stylesheet" href="/static/bb/[0-9a-f]+/css/bundle\.css">`, body)
	for _, p := range []string{"/agent/6f9a3c1e-0000-4000-8000-000000000001"} {
		resp, body := pageGet(t, c, "GET", ts.URL+p)
		require.Equal(t, 200, resp.StatusCode, p)
		assertHintsEveryStartupModule(t, p, body)
		assert.Contains(t, body, `/static/b/`, "the pop-out loads the original files")
		assert.NotContains(t, body, `/static/bb/`, "and not the bundle family")
	}
}

// CORAL_NO_BUNDLE=1 serves the original files (debugging): 24 stylesheets, the module hints, /static/b/.
func TestNoBundleSwitchServesTheOriginalFiles(t *testing.T) {
	t.Setenv("CORAL_NO_BUNDLE", "1")
	_, ts, _ := strictServer(t, false)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{"/", "/agents/6f9a3c1e-0000-4000-8000-000000000001"} {
		resp, body := pageGet(t, c, "GET", ts.URL+p)
		require.Equal(t, 200, resp.StatusCode, p)
		assertHintsEveryStartupModule(t, p, body)
		assert.Greater(t, strings.Count(body, `rel="stylesheet"`), 20, p)
		assert.NotContains(t, body, `/static/bb/`)
		r2, _ := pageGet(t, c, "GET", ts.URL+"/static/bb/anything/app.js")
		assert.Equal(t, 404, r2.StatusCode, "no bundle family without the bundle")
	}
}
