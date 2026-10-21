package server

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/frontend"
)

// copyTree is the embedded static tree as a MapFS a test may change.
func copyTree(t *testing.T, fsys fs.FS) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	require.NoError(t, fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, name)
		out[name] = &fstest.MapFile{Data: b}
		return err
	}))
	return out
}

// #4320 (Security's SHOULD 3 in #4319): /static/bb/<ver>/ is served immutable for a year, so <ver> names the
// bytes it serves: a changed bundle input, a different stylesheet order, or another staticVersion
// is another version, and an unchanged build is the same one.
func TestBundleVersionNamesTheBytesItServes(t *testing.T) {
	staticSub, err := fs.Sub(staticFS, "static")
	require.NoError(t, err)
	index, err := fs.ReadFile(frontend.Templates, "templates/index.html")
	require.NoError(t, err)
	build := func(tree fs.FS, index []byte, staticVersion string) string {
		b, err := frontend.BuildBundle(tree, index)
		require.NoError(t, err)
		return bundleVersion(staticVersion, b)
	}

	base := build(staticSub, index, "v1")
	assert.Regexp(t, `^[0-9a-f]{12}$`, base)
	assert.Equal(t, base, build(staticSub, index, "v1"), "an unchanged build keeps its version")
	assert.NotEqual(t, base, build(staticSub, index, "v2"), "the tree and minifier version is an input")

	// One byte of a module the bundle contains.
	tree := copyTree(t, staticSub)
	b, err := frontend.BuildBundle(staticSub, index)
	require.NoError(t, err)
	require.NotEmpty(t, b.Modules)
	mod := b.Modules[0]
	tree[mod] = &fstest.MapFile{Data: append(append([]byte{}, tree[mod].Data...), []byte("\n// one more byte\nexport const zz4320 = 1;\n")...)}
	assert.NotEqual(t, base, build(tree, index, "v1"), "a changed module changes the version")

	// The stylesheet order in the shell.
	links := regexp.MustCompile(`(?m)^.*<link rel="stylesheet" href="\{\{ \$base \}\}css/[^"]+\.css">.*$`).FindAllString(string(index), -1)
	require.GreaterOrEqual(t, len(links), 2)
	swapped := strings.Replace(string(index), links[0], "\x00A", 1)
	swapped = strings.Replace(swapped, links[1], links[0], 1)
	swapped = strings.Replace(swapped, "\x00A", links[1], 1)
	require.NotEqual(t, string(index), swapped)
	assert.NotEqual(t, base, build(staticSub, []byte(swapped), "v1"), "the stylesheet order changes the version")
}

// #4528: two hand-built servers were reported to serve the same bundle for two trees that differ in one JS file. They cannot:
// a function added to a bundled module (live_chat.js, as #4518 did) changes the tree's version and the bundle's, and the bundle
// the new version names carries the new code. (The report's hash was the unpatched tree's: both servers ran unpatched code.)
func TestAChangedModuleIsServedUnderANewBuild(t *testing.T) {
	staticSub, err := fs.Sub(staticFS, "static")
	require.NoError(t, err)
	index, err := fs.ReadFile(frontend.Templates, "templates/index.html")
	require.NoError(t, err)
	serve := func(tree fs.FS) (string, *frontend.Bundle) {
		v, err := staticBuildVersion(tree)
		require.NoError(t, err)
		b, err := frontend.BuildBundle(tree, index)
		require.NoError(t, err)
		return bundleVersion(v, b), b
	}
	base, b0 := serve(staticSub)
	tree := copyTree(t, staticSub)
	require.Contains(t, tree, "live_chat.js")
	tree["live_chat.js"] = &fstest.MapFile{Data: append(append([]byte{}, tree["live_chat.js"].Data...),
		[]byte("\nexport function zz4528Prefetch() { return 'zz4528-marker'; }\n")...)}
	changed, b1 := serve(tree)
	assert.NotEqual(t, base, changed, "a changed module is served under another build id")
	has := func(b *frontend.Bundle) bool {
		for _, data := range b.Files {
			if strings.Contains(string(data), "zz4528-marker") {
				return true
			}
		}
		return false
	}
	assert.False(t, has(b0), "the unchanged tree's bundle has no marker")
	assert.True(t, has(b1), "the changed tree's bundle carries the new code")
}

// The /static/b/ version hashes the minifier too: the same files through another esbuild or other
// options (or with minifying off) are other bytes.
func TestStaticBuildVersionIncludesTheMinifier(t *testing.T) {
	staticSub, err := fs.Sub(staticFS, "static")
	require.NoError(t, err)
	t.Setenv("CORAL_NO_MINIFY", "")
	on, err := staticBuildVersion(staticSub)
	require.NoError(t, err)
	again, err := staticBuildVersion(staticSub)
	require.NoError(t, err)
	assert.Equal(t, on, again)
	t.Setenv("CORAL_NO_MINIFY", "1")
	off, err := staticBuildVersion(staticSub)
	require.NoError(t, err)
	assert.NotEqual(t, on, off, "CORAL_NO_MINIFY changes the bytes, so the version")
}

// Every static response carries nosniff, and /static files carry CORP same-origin (#4320, INFO 5).
func TestStaticResponsesCarryNosniffAndCORP(t *testing.T) {
	fsys := fstest.MapFS{"app.js": {Data: []byte("export const a = 1;\n")}, "css/x.css": {Data: []byte("a{color:red}\n")}}
	get := func(h http.Handler, path string) *http.Response {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Result()
	}
	plain := staticAssetHandler(fsys, http.NotFoundHandler(), cacheRevalidate)
	versioned := versionedStaticHandlerAt("/static/bb/", fsys, "cur123")
	for name, resp := range map[string]*http.Response{
		"plain":            get(plain, "/app.js"),
		"immutable":        get(versioned, "/static/bb/cur123/app.js"),
		"stale version":    get(versioned, "/static/bb/old000/css/x.css"),
		"gzip-less or not": get(plain, "/css/x.css"),
	} {
		assert.Equal(t, http.StatusOK, resp.StatusCode, name)
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"), name)
		assert.Equal(t, "same-origin", resp.Header.Get("Cross-Origin-Resource-Policy"), name)
	}
	assert.Contains(t, get(versioned, "/static/bb/cur123/app.js").Header.Get("Cache-Control"), "immutable")
	assert.NotContains(t, get(versioned, "/static/bb/old000/app.js").Header.Get("Cache-Control"), "immutable")

	// The rendered pages and the manifest go through serveCachedAsset: nosniff there too.
	var p renderedPage
	rec := httptest.NewRecorder()
	require.NoError(t, p.serve(rec, httptest.NewRequest(http.MethodGet, "/", nil), "k", func(w io.Writer) error {
		_, err := w.Write([]byte("<html></html>"))
		return err
	}))
	assert.Equal(t, "nosniff", rec.Result().Header.Get("X-Content-Type-Options"))
}

// Through the real router: the build id is ONE name for the bundled family, the pop-out's family and
// the X-Coral-Build header the update banner compares with (a page whose build differs from the
// header shows the "Coral was updated" banner), and it is the version those prefixes serve as current
// (immutable): any other version is revalidated, never cached for a year.
func TestDashboardBuildIdIsTheOneServedAsCurrent(t *testing.T) {
	srv, ts, _ := strictServer(t, false)
	require.NotEmpty(t, srv.bundleBase, "the bundled family is on")
	do := func(path string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+srv.keyStore.Key())
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	_, page := do("/")
	m := regexp.MustCompile(`/static/bb/([0-9a-f]{12})/app\.js`).FindSubmatch(page)
	require.NotNil(t, m, "the page loads app.js from the bundled prefix")
	ver := string(m[1])
	assert.Equal(t, srv.staticVersion, ver)
	api, _ := do("/api/health")
	assert.Equal(t, ver, api.Header.Get("X-Coral-Build"), "the header carries the id the page loaded from, or the update banner shows")

	for _, prefix := range []string{"/static/bb/", "/static/b/"} {
		cur, _ := do(prefix + ver + "/app.js")
		assert.Equal(t, http.StatusOK, cur.StatusCode, prefix)
		assert.Contains(t, cur.Header.Get("Cache-Control"), "immutable", prefix+": the current build")
		assert.Equal(t, "nosniff", cur.Header.Get("X-Content-Type-Options"), prefix)
		assert.Equal(t, "same-origin", cur.Header.Get("Cross-Origin-Resource-Policy"), prefix)
		old, _ := do(prefix + "000000000000/app.js")
		assert.NotContains(t, old.Header.Get("Cache-Control"), "immutable", prefix+": another version is revalidated")
	}
}
