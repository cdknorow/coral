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

// The dashboard shell hints the whole startup module graph with <link rel="modulepreload"> (#4131). Without the hints a browser
// discovers the 130-odd modules one import level at a time, which on a slow link is most of the load: PageData.ModulePreloads was
// defined (#3900) but only an embedder set it, and the standalone server did not.
func TestShellHintsTheStartupModules(t *testing.T) {
	_, ts, _ := strictServer(t, false)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	want := frontend.StartupModules()
	require.Greater(t, len(want), 100, "the startup graph is the real one")
	re := regexp.MustCompile(`<link rel="modulepreload" href="([^"]+)">`)
	for _, p := range []string{"/", "/agents/6f9a3c1e-0000-4000-8000-000000000001", "/agent/6f9a3c1e-0000-4000-8000-000000000001"} {
		resp, body := pageGet(t, c, "GET", ts.URL+p)
		require.Equal(t, 200, resp.StatusCode, p)
		got := map[string]bool{}
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			got[m[1]] = true
		}
		assert.Len(t, got, len(want), "%s: one hint per startup module", p)
		for _, m := range want {
			found := false
			for h := range got {
				if strings.HasSuffix(h, "/"+m) && strings.HasPrefix(h, "/static/") {
					found = true
					break
				}
			}
			assert.True(t, found, "%s: a hint for %s under /static/", p, m)
		}
		// Hints come after the stylesheets, so the render-blocking CSS is asked for first.
		assert.Less(t, strings.LastIndex(body, `rel="stylesheet"`), strings.Index(body, `rel="modulepreload"`), "%s: stylesheets before the module hints", p)
	}
}
