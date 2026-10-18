package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/pkg/frontend"
)

func staticTestServer(t *testing.T) (*httptest.Server, fs.FS) {
	t.Helper()
	sub, err := fs.Sub(frontend.Static, "static")
	require.NoError(t, err)
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fallback", "1")
		http.FileServer(http.FS(sub)).ServeHTTP(w, r)
	})
	srv := httptest.NewServer(http.StripPrefix("/static/", staticAssetHandler(sub, fallback, cacheRevalidate)))
	t.Cleanup(srv.Close)
	return srv, sub
}

// get requests url with the given headers; Accept-Encoding is set explicitly so
// the Go client leaves a gzip body encoded.
func get(t *testing.T, method, url string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", "identity")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, body
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	require.NoError(t, err)
	out, err := io.ReadAll(zr)
	require.NoError(t, err)
	return out
}

// Text assets: gzip when accepted (smaller, decodes to the file), plain
// otherwise; both carry an ETag and Cache-Control: no-cache (never no-store),
// and Vary: Accept-Encoding. The gzip and plain forms have different ETags.
func TestStaticAssetsGzipAndRevalidate(t *testing.T) {
	srv, sub := staticTestServer(t)
	want, err := fs.ReadFile(sub, "app.js")
	require.NoError(t, err)
	want = frontend.MinifyAsset("app.js", want) // the server serves the minified source (#4203)

	plain, body := get(t, http.MethodGet, srv.URL+"/static/app.js", nil)
	require.Equal(t, http.StatusOK, plain.StatusCode)
	assert.Equal(t, want, body)
	assert.Empty(t, plain.Header.Get("Content-Encoding"))
	assert.Equal(t, "no-cache", plain.Header.Get("Cache-Control"))
	assert.NotContains(t, plain.Header.Get("Cache-Control"), "no-store")
	assert.Empty(t, plain.Header.Get("Pragma"))
	assert.Contains(t, plain.Header.Values("Vary"), "Accept-Encoding")
	assert.Contains(t, plain.Header.Get("Content-Type"), "javascript")
	plainTag := plain.Header.Get("ETag")
	require.Regexp(t, `^"[0-9a-f]{24}"$`, plainTag)

	gz, gzBody := get(t, http.MethodGet, srv.URL+"/static/app.js", map[string]string{"Accept-Encoding": "gzip, deflate, br"})
	require.Equal(t, http.StatusOK, gz.StatusCode)
	assert.Equal(t, "gzip", gz.Header.Get("Content-Encoding"))
	assert.Less(t, len(gzBody), len(want))
	assert.Equal(t, want, gunzip(t, gzBody))
	gzTag := gz.Header.Get("ETag")
	assert.NotEqual(t, plainTag, gzTag, "each representation has its own validator")
	assert.Equal(t, "no-cache", gz.Header.Get("Cache-Control"))

	// Revalidation: a matching If-None-Match is a 304 with no body, for either form.
	nm, nmBody := get(t, http.MethodGet, srv.URL+"/static/app.js", map[string]string{"If-None-Match": plainTag})
	assert.Equal(t, http.StatusNotModified, nm.StatusCode)
	assert.Empty(t, nmBody)
	assert.Equal(t, plainTag, nm.Header.Get("ETag"))
	nmgz, _ := get(t, http.MethodGet, srv.URL+"/static/app.js", map[string]string{"If-None-Match": gzTag, "Accept-Encoding": "gzip"})
	assert.Equal(t, http.StatusNotModified, nmgz.StatusCode)
	stale, _ := get(t, http.MethodGet, srv.URL+"/static/app.js", map[string]string{"If-None-Match": `"0000"`})
	assert.Equal(t, http.StatusOK, stale.StatusCode, "a different validator gets the file")

	// A range request is served from the plain bytes.
	rg, rgBody := get(t, http.MethodGet, srv.URL+"/static/app.js", map[string]string{"Range": "bytes=0-9", "Accept-Encoding": "gzip"})
	assert.Equal(t, http.StatusPartialContent, rg.StatusCode)
	assert.Empty(t, rg.Header.Get("Content-Encoding"))
	assert.Equal(t, want[:10], rgBody)

	head, headBody := get(t, http.MethodHead, srv.URL+"/static/app.js", map[string]string{"Accept-Encoding": "gzip"})
	assert.Equal(t, http.StatusOK, head.StatusCode)
	assert.Empty(t, headBody)
	assert.Equal(t, "gzip", head.Header.Get("Content-Encoding"))
}

// Already-compressed types are never gzipped but still revalidate; directories
// and missing files fall through to the previous handler.
func TestStaticAssetsSkipCompressedTypesAndFallThrough(t *testing.T) {
	srv, _ := staticTestServer(t)
	png, _ := get(t, http.MethodGet, srv.URL+"/static/favicon.png", map[string]string{"Accept-Encoding": "gzip"})
	require.Equal(t, http.StatusOK, png.StatusCode)
	assert.Empty(t, png.Header.Get("Content-Encoding"))
	assert.Empty(t, png.Header.Values("Vary"), "nothing varies for a type that is never compressed")
	assert.Equal(t, "image/png", png.Header.Get("Content-Type"))
	assert.NotEmpty(t, png.Header.Get("ETag"))

	dir, _ := get(t, http.MethodGet, srv.URL+"/static/vendor/", nil)
	assert.Equal(t, "1", dir.Header.Get("X-Fallback"), "a directory is the previous handler's")
	missing, _ := get(t, http.MethodGet, srv.URL+"/static/no-such-file.js", nil)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode)
	assert.Equal(t, "1", missing.Header.Get("X-Fallback"))

	// Path cleaning cannot escape the static tree.
	esc, _ := get(t, http.MethodGet, srv.URL+"/static/..%2f..%2fgo.mod", nil)
	assert.NotEqual(t, http.StatusOK, esc.StatusCode)
}

// The dashboard page is gzipped when accepted, revalidates with an ETag and
// no-cache, and is rendered once for the same template data.
func TestIndexPageGzipAndRevalidate(t *testing.T) {
	tmpl, err := frontend.ParseIndex()
	require.NoError(t, err)
	s := &Server{cfg: &config.Config{CoralRoot: "/srv/coral"}, indexTmpl: tmpl}

	serve := func(hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rr := httptest.NewRecorder()
		s.serveIndex(rr, req)
		return rr
	}
	plain := serve(nil)
	require.Equal(t, http.StatusOK, plain.Code)
	assert.Equal(t, "text/html; charset=utf-8", plain.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", plain.Header().Get("Cache-Control"))
	assert.Empty(t, plain.Header().Get("Content-Encoding"))
	assert.Contains(t, plain.Body.String(), "/srv/coral")
	assert.True(t, strings.HasPrefix(strings.TrimSpace(plain.Body.String()), "<!DOCTYPE html>") || strings.Contains(plain.Body.String(), "<html"))

	gz := serve(map[string]string{"Accept-Encoding": "gzip"})
	assert.Equal(t, "gzip", gz.Header().Get("Content-Encoding"))
	assert.Equal(t, plain.Body.Bytes(), gunzip(t, gz.Body.Bytes()))
	assert.Less(t, gz.Body.Len(), plain.Body.Len()/3)

	nm := serve(map[string]string{"If-None-Match": plain.Header().Get("ETag")})
	assert.Equal(t, http.StatusNotModified, nm.Code)
	first := s.indexPage.page
	serve(nil)
	assert.Same(t, first, s.indexPage.page, "the same data is not rendered again")
	s.cfg.CoralRoot = "/other"
	assert.Contains(t, serve(nil).Body.String(), "/other", "new data renders a new page")
}

// The vendor scripts in the page head are deferred, so they do not block the
// first paint; the module app.js still runs after them.
func TestIndexDefersVendorScripts(t *testing.T) {
	page, err := fs.ReadFile(frontend.Templates, "templates/index.html")
	require.NoError(t, err)
	head := string(page)[:strings.Index(string(page), "</head>")]
	n := 0
	for _, line := range strings.Split(head, "\n") {
		if strings.Contains(line, "<script") && strings.Contains(line, "vendor/") {
			n++
			assert.Contains(t, line, " defer ", "parser-blocking vendor script in <head>: %s", strings.TrimSpace(line))
		}
	}
	// CodeMirror, xterm (#3083) and highlight.js (#3117) load on first use
	// (vendor_loader.js): the head loads no vendor script at all.
	assert.Zero(t, n, "vendor scripts in <head>")
	assert.NotContains(t, head, "vendor/hljs/", "highlight.js and its theme load on first use")
	assert.Contains(t, string(page), `<script type="module" src="{{ $base }}app.js"></script>`)
}

// /static/b/<build>/ serves the same files as /static/, immutable for the
// current build. Another build's prefix (a page from before an upgrade) still
// gets the current files, but revalidating, so a stale name is never pinned.
func TestVersionedStaticPaths(t *testing.T) {
	sub, err := fs.Sub(frontend.Static, "static")
	require.NoError(t, err)
	ver, err := staticBuildVersion(sub)
	require.NoError(t, err)
	require.Regexp(t, `^[0-9a-f]{12}$`, ver)
	again, err := staticBuildVersion(sub)
	require.NoError(t, err)
	require.Equal(t, ver, again, "the build version is deterministic")

	srv := httptest.NewServer(versionedStaticHandler(sub, ver))
	t.Cleanup(srv.Close)
	want, err := fs.ReadFile(sub, "app.js")
	require.NoError(t, err)
	want = frontend.MinifyAsset("app.js", want) // the server serves the minified source (#4203)

	cur, body := get(t, http.MethodGet, srv.URL+"/static/b/"+ver+"/app.js", map[string]string{"Accept-Encoding": "gzip"})
	require.Equal(t, http.StatusOK, cur.StatusCode)
	assert.Equal(t, "public, max-age=31536000, immutable", cur.Header.Get("Cache-Control"))
	assert.Equal(t, "gzip", cur.Header.Get("Content-Encoding"))
	assert.Equal(t, want, gunzip(t, body))
	nested, _ := get(t, http.MethodGet, srv.URL+"/static/b/"+ver+"/css/touch.css", nil)
	assert.Equal(t, http.StatusOK, nested.StatusCode, "nested paths resolve")

	old, oldBody := get(t, http.MethodGet, srv.URL+"/static/b/000000000000/app.js", nil)
	require.Equal(t, http.StatusOK, old.StatusCode)
	assert.Equal(t, "no-cache", old.Header.Get("Cache-Control"), "another build's name is never cached for good")
	assert.Equal(t, want, oldBody)

	for _, bad := range []string{"/static/b/", "/static/b/" + ver, "/static/b/" + ver + "/", "/static/b/" + ver + "/no-such.js"} {
		resp, _ := get(t, http.MethodGet, srv.URL+bad, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, bad)
	}
}

// Every static URL in the shell carries the versioned prefix, so relative module
// imports, CSS url()s and import.meta.url-based URLs inherit it. A renderer that
// sets no StaticBase (an older Cloud Hub) gets the plain /static/ paths.
func TestIndexUsesVersionedStaticBase(t *testing.T) {
	tmpl, err := frontend.ParseIndex()
	require.NoError(t, err)
	s := &Server{cfg: &config.Config{CoralRoot: "/srv/coral"}, indexTmpl: tmpl, staticVersion: "abcdef012345"}
	rr := httptest.NewRecorder()
	s.serveIndex(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	page := rr.Body.String()
	assert.Contains(t, page, `<script type="module" src="/static/b/abcdef012345/app.js"></script>`)
	assert.Contains(t, page, `href="/static/b/abcdef012345/css/base.css"`)
	assert.Contains(t, page, `src="/static/b/abcdef012345/vendor/marked.min.js"`)
	// The editor and terminal libraries load on first use (vendor_loader.js, #3083).
	assert.NotContains(t, page, "vendor/xterm/")
	assert.NotContains(t, page, "vendor/codemirror/")
	assert.Contains(t, page, `href="/static/b/abcdef012345/manifest.json"`)
	assert.Equal(t, strings.Count(page, `"/static/`), strings.Count(page, `"/static/b/abcdef012345/`), "every static URL is versioned")

	var plain bytes.Buffer
	require.NoError(t, tmpl.Execute(&plain, frontend.PageData{CoralRoot: "/srv/coral"}))
	assert.Contains(t, plain.String(), `<script type="module" src="/static/app.js"></script>`)
	assert.NotContains(t, plain.String(), "/static/b/")
}

// The manifest names its icons relatively, so the copy linked under the
// versioned prefix fetches them under it (no unversioned icon-512.png). The root
// /manifest.json anchors the same icons at /static/ so they still resolve.
func TestManifestIconsFollowTheManifestURL(t *testing.T) {
	sub, err := fs.Sub(frontend.Static, "static")
	require.NoError(t, err)
	icons := func(data []byte) []string {
		var m struct {
			Name  string `json:"name"`
			Icons []struct {
				Src string `json:"src"`
			} `json:"icons"`
		}
		require.NoError(t, json.Unmarshal(data, &m))
		require.Equal(t, "Coral", m.Name)
		var out []string
		for _, i := range m.Icons {
			out = append(out, i.Src)
		}
		require.NotEmpty(t, out)
		return out
	}

	embedded, err := fs.ReadFile(sub, "manifest.json")
	require.NoError(t, err)
	base, _ := url.Parse("http://h/static/b/abcdef012345/manifest.json")
	for _, src := range icons(embedded) {
		ref, err := url.Parse(src)
		require.NoError(t, err)
		resolved := base.ResolveReference(ref).Path
		assert.True(t, strings.HasPrefix(resolved, "/static/b/abcdef012345/"), "%s resolves to %s", src, resolved)
		_, err = fs.Stat(sub, strings.TrimPrefix(resolved, "/static/b/abcdef012345/"))
		assert.NoError(t, err, "icon %s exists", src)
	}

	srv := httptest.NewServer(rootManifestHandler(frontend.Static))
	t.Cleanup(srv.Close)
	resp, body := get(t, http.MethodGet, srv.URL+"/manifest.json", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/manifest+json", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	assert.NotEmpty(t, resp.Header.Get("ETag"))
	root := icons(body)
	assert.Contains(t, root, "/static/icon-512.png")
	for _, src := range root {
		require.True(t, strings.HasPrefix(src, "/static/"), src)
		_, err := fs.Stat(sub, strings.TrimPrefix(src, "/static/"))
		assert.NoError(t, err, "icon %s exists", src)
	}
}
