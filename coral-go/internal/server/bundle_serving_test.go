package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/pkg/frontend"
)

const bundleTestVersion = "abcdef012345"

func bundleTestServer(t *testing.T) *Server {
	t.Helper()
	tmpl, err := frontend.ParseIndex()
	require.NoError(t, err)
	return &Server{cfg: &config.Config{CoralRoot: "/srv/coral"}, indexTmpl: tmpl, staticVersion: bundleTestVersion, bundleBase: "/static/bb/" + bundleTestVersion + "/"}
}

// With the bundled family on, the dashboard starts from one script and one stylesheet under /static/bb/<build>/.
func TestDashboardUsesTheBundledFamily(t *testing.T) {
	s := bundleTestServer(t)
	rr := httptest.NewRecorder()
	s.serveIndex(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	page := rr.Body.String()
	assert.Contains(t, page, `<script type="module" src="/static/bb/`+bundleTestVersion+`/app.js"></script>`)
	assert.Equal(t, 1, strings.Count(page, `rel="stylesheet"`), "one stylesheet link, not 24")
	assert.Contains(t, page, `<link rel="stylesheet" href="/static/bb/`+bundleTestVersion+`/css/bundle.css">`)
	assert.NotContains(t, page, "css/base.css")
	assert.Equal(t, strings.Count(page, `"/static/`), strings.Count(page, `"/static/bb/`+bundleTestVersion+`/`), "every static URL is under the bundled prefix")
}

// The agent pop-out is the same app.js in another mode: it stays on the original files and their 24 stylesheets, because a stub at an
// original URL would load the dashboard's boot code.
func TestAgentPopoutStaysOnTheOriginalFiles(t *testing.T) {
	s := bundleTestServer(t)
	r := chi.NewRouter()
	r.Get("/agent/{sessionID}", s.serveAgentPopout)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/agent/123e4567-e89b-12d3-a456-426614174000", nil))
	page := rr.Body.String()
	assert.Contains(t, page, `<script type="module" src="/static/b/`+bundleTestVersion+`/app.js"></script>`)
	assert.NotContains(t, page, "/static/bb/")
	assert.NotContains(t, page, "css/bundle.css")
	assert.GreaterOrEqual(t, strings.Count(page, `rel="stylesheet"`), 20)
}

// Without a bundle (CORAL_NO_BUNDLE, a build error, no version) the dashboard is as it was before #4228.
func TestDashboardWithoutABundleUsesTheOriginalFiles(t *testing.T) {
	s := bundleTestServer(t)
	s.bundleBase = ""
	rr := httptest.NewRecorder()
	s.serveIndex(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	page := rr.Body.String()
	assert.Contains(t, page, `<script type="module" src="/static/b/`+bundleTestVersion+`/app.js"></script>`)
	assert.NotContains(t, page, "/static/bb/")
	assert.GreaterOrEqual(t, strings.Count(page, `rel="stylesheet"`), 20)
}

// /static/bb/<build>/ serves the bundle's files (immutable for the current build, revalidated for another) and the original for
// every other path.
func TestBundledPrefixServesTheBundleOverTheOriginals(t *testing.T) {
	sub, err := fs.Sub(frontend.Static, "static")
	require.NoError(t, err)
	index, err := fs.ReadFile(frontend.Templates, "templates/index.html")
	require.NoError(t, err)
	b, err := frontend.BuildBundle(sub, index)
	require.NoError(t, err)
	srv := httptest.NewServer(versionedStaticHandlerAt("/static/bb/", b.FS(sub), bundleTestVersion))
	t.Cleanup(srv.Close)

	app, body := get(t, http.MethodGet, srv.URL+"/static/bb/"+bundleTestVersion+"/app.js", nil)
	require.Equal(t, http.StatusOK, app.StatusCode)
	assert.Equal(t, "public, max-age=31536000, immutable", app.Header.Get("Cache-Control"))
	assert.Contains(t, app.Header.Get("Content-Type"), "javascript")
	assert.Contains(t, string(body), "sourceMappingURL=app.js.map")
	assert.Less(t, len(body), 1_500_000)
	assert.Greater(t, len(body), 500_000, "the whole startup graph is in app.js")

	stub, stubBody := get(t, http.MethodGet, srv.URL+"/static/bb/"+bundleTestVersion+"/state.js", nil)
	require.Equal(t, http.StatusOK, stub.StatusCode)
	assert.Regexp(t, regexp.MustCompile(`^export \{.*\} from '\./app\.js';\n?$`), string(stubBody), "state.js is a stub of app.js")
	assert.Less(t, len(stubBody), 4096)

	css, _ := get(t, http.MethodGet, srv.URL+"/static/bb/"+bundleTestVersion+"/css/bundle.css", nil)
	assert.Equal(t, http.StatusOK, css.StatusCode)
	assert.Contains(t, css.Header.Get("Content-Type"), "css")
	font, _ := get(t, http.MethodGet, srv.URL+"/static/bb/"+bundleTestVersion+"/vendor/fonts/jakarta-latin.woff2", nil)
	assert.Equal(t, http.StatusOK, font.StatusCode, "an original file the bundle does not replace (a font the stylesheet names relatively)")
	lazy, _ := get(t, http.MethodGet, srv.URL+"/static/bb/"+bundleTestVersion+"/password_ui.js", nil)
	assert.Equal(t, http.StatusOK, lazy.StatusCode, "a lazily loaded module is the original")

	old, _ := get(t, http.MethodGet, srv.URL+"/static/bb/000000000000/app.js", nil)
	require.Equal(t, http.StatusOK, old.StatusCode)
	assert.Equal(t, "no-cache", old.Header.Get("Cache-Control"), "another build's name is never cached for good")
	for _, bad := range []string{"/static/bb/", "/static/bb/" + bundleTestVersion, "/static/bb/" + bundleTestVersion + "/no-such.js"} {
		resp, _ := get(t, http.MethodGet, srv.URL+bad, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, bad)
	}
}
