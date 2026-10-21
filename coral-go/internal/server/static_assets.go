package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/pkg/frontend"
)

// The embedded /static tree never changes while the server runs, so each
// file's validator and compressed form are computed once (on first request)
// and reused: an ETag from the content hash, revalidated with
// Cache-Control: no-cache (a repeat visit costs a 304, not the bytes), and a
// gzip copy for text types sent when the client accepts it. Behind the Cloud
// Hub tunnel the connector never forwards Accept-Encoding, so those clients
// get the plain bytes, as before.

// staticAsset is one embedded file, ready to serve.
type staticAsset struct {
	contentType string
	plain       []byte
	gz          []byte // nil when not worth compressing
	etag        string // quoted strong validator of the plain bytes
	gzEtag      string // validator of the gzip representation
}

// compressibleExt lists text types worth gzipping; images and fonts are
// already compressed.
var compressibleExt = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".svg": true, ".json": true,
	".html": true, ".htm": true, ".map": true, ".txt": true, ".md": true, ".xml": true,
	".webmanifest": true,
}

// staticMinGzip is the smallest file gzipped; below it the header outweighs the saving.
const staticMinGzip = 512

func newStaticAsset(name string, data []byte) *staticAsset {
	return newStaticAssetFrom(name, frontend.MinifyAsset(name, data)) // comments and indentation are bytes on the wire (#4203)
}

// newStaticAssetFrom is newStaticAsset for bytes that are final already (the bundled family's files, #4228: minified, with their source map link).
func newStaticAssetFrom(name string, data []byte) *staticAsset {
	sum := sha256.Sum256(data)
	tag := hex.EncodeToString(sum[:12])
	a := &staticAsset{plain: data, etag: `"` + tag + `"`}
	ext := strings.ToLower(path.Ext(name))
	a.contentType = mime.TypeByExtension(ext)
	if a.contentType == "" {
		a.contentType = http.DetectContentType(data)
	}
	if compressibleExt[ext] && len(data) >= staticMinGzip {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(data)
		_ = zw.Close()
		if buf.Len() < len(data) {
			a.gz = buf.Bytes()
			a.gzEtag = `"` + tag + `-gz"`
		}
	}
	return a
}

// Cache policies for static files: plain /static/ paths revalidate; the
// build-versioned /static/b/<build>/ paths never change, so browsers keep them.
const (
	cacheRevalidate = "no-cache"
	cacheImmutable  = "public, max-age=31536000, immutable"
)

// staticBuildVersion is a short content hash of the whole static tree: it
// changes whenever any file changes, so a versioned URL always names one exact
// set of files. It is computed once at startup (the tree is embedded).
func staticBuildVersion(fsys fs.FS) (string, error) {
	h := sha256.New()
	// The served bytes are the tree through the minifier, so the minifier is an input: a new esbuild
	// or other options with the same files would otherwise ship new bytes under the same
	// immutable URL, kept by browsers for a year (#4320).
	io.WriteString(h, frontend.MinifyIdentity())
	h.Write([]byte{0})
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		io.WriteString(h, name)
		h.Write([]byte{0})
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)[:6]), nil
}

// bundleVersion is the version of the bundled family (/static/bb/<ver>/): a hash of the bytes it
// serves, not of the sources it was built from. The build's output also depends on the bundler and
// its options and on the stylesheet order in the shell, none of which are in the static tree, so
// the hash is over the staticVersion (the tree and the minifier) and every built file, in name
// order (#4320).
func bundleVersion(staticVersion string, b *frontend.Bundle) string {
	h := sha256.New()
	io.WriteString(h, staticVersion)
	h.Write([]byte{0})
	names := make([]string, 0, len(b.Files))
	for name := range b.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		io.WriteString(h, name)
		h.Write([]byte{0})
		fmt.Fprintf(h, "%d", len(b.Files[name]))
		h.Write([]byte{0})
		h.Write(b.Files[name])
	}
	return hex.EncodeToString(h.Sum(nil)[:6])
}

// versionedStaticHandler serves /static/b/{ver}/* from fsys. The current
// build is immutable; any other version (a page loaded before an upgrade)
// still gets the current files, but only with revalidation, so a stale name
// can never be cached forever with the wrong bytes.
func versionedStaticHandler(fsys fs.FS, current string) http.Handler {
	return versionedStaticHandlerAt("/static/b/", fsys, current)
}

// versionedStaticHandlerAt is versionedStaticHandler under another prefix: the bundled family (#4228) is served at /static/bb/{ver}/.
func versionedStaticHandlerAt(prefix string, fsys fs.FS, current string) http.Handler {
	immutable := staticAssetHandler(fsys, http.NotFoundHandler(), cacheImmutable)
	revalidate := staticAssetHandler(fsys, http.NotFoundHandler(), cacheRevalidate)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		ver, file, ok := strings.Cut(rest, "/")
		if !ok || ver == "" || file == "" {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = file
		r2.URL.RawPath = ""
		if ver == current {
			immutable.ServeHTTP(w, r2)
		} else {
			revalidate.ServeHTTP(w, r2)
		}
	})
}

// staticAssetHandler serves files from fsys (the embedded static tree, already
// stripped of its /static/ prefix) with the given Cache-Control. Directories
// and missing files fall through to next, which keeps http.FileServer's
// behaviour for them.
func staticAssetHandler(fsys fs.FS, next http.Handler, cacheControl string) http.Handler {
	var cache sync.Map // clean path -> *staticAsset
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		v, ok := cache.Load(name)
		if !ok {
			st, err := fs.Stat(fsys, name)
			if err != nil || st.IsDir() {
				next.ServeHTTP(w, r)
				return
			}
			data, err := fs.ReadFile(fsys, name)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			// A file the bundled family built itself is served as built: minifying it again would drop its source map link.
			if built, ok := fsys.(interface{ Built(string) bool }); ok && built.Built(name) {
				v, _ = cache.LoadOrStore(name, newStaticAssetFrom(name, data))
			} else {
				v, _ = cache.LoadOrStore(name, newStaticAsset(name, data))
			}
		}
		// Files under /static are only ever this origin's own: never sniffed into another type,
		// never loaded by another origin's page (#4320, Security's INFO 5).
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		serveCachedAsset(w, r, v.(*staticAsset), cacheControl)
	})
}

// serveCachedAsset writes a, gzipped when the client accepts it and asked for
// the whole file. http.ServeContent answers If-None-Match with 304 against the
// ETag of the representation being sent, and serves ranges and HEAD.
func serveCachedAsset(w http.ResponseWriter, r *http.Request, a *staticAsset, cacheControl string) {
	h := w.Header()
	h.Set("Content-Type", a.contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", cacheControl)
	body, etag := a.plain, a.etag
	if a.gz != nil {
		h.Add("Vary", "Accept-Encoding")
		if r.Header.Get("Range") == "" && httputil.AcceptsGzip(r) {
			h.Set("Content-Encoding", "gzip")
			body, etag = a.gz, a.gzEtag
		}
	}
	h.Set("ETag", etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// renderedPage caches a server-rendered page whose bytes depend only on a
// key (the template data): its gzip copy and validators are computed once per
// key instead of on every load.
type renderedPage struct {
	mu   sync.Mutex
	key  string
	page *staticAsset
}

// serve renders with render when the key changed, then serves the cached page
// with no-cache revalidation (gzip when accepted). A render error is reported
// and nothing is cached.
func (p *renderedPage) serve(w http.ResponseWriter, r *http.Request, key string, render func(io.Writer) error) error {
	p.mu.Lock()
	page := p.page
	if page == nil || p.key != key {
		var buf bytes.Buffer
		if err := render(&buf); err != nil {
			p.mu.Unlock()
			return err
		}
		page = newStaticAsset("page.html", buf.Bytes())
		page.contentType = "text/html; charset=utf-8"
		p.key, p.page = key, page
	}
	p.mu.Unlock()
	serveCachedAsset(w, r, page, cacheRevalidate)
	return nil
}

// rootManifestHandler serves the PWA manifest at /manifest.json. The embedded
// manifest's icon URLs are relative, so a manifest loaded from /static/ or a
// versioned /static/b/<build>/ fetches icons under that same prefix; at the
// root they would resolve to /icon-512.png, so this copy anchors them at /static/.
func rootManifestHandler(fsys fs.FS) http.HandlerFunc {
	var (
		once  sync.Once
		asset *staticAsset
	)
	return func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			data, err := fs.ReadFile(fsys, "static/manifest.json")
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(data, &m) == nil {
				if icons, ok := m["icons"].([]any); ok {
					for _, ic := range icons {
						if icon, ok := ic.(map[string]any); ok {
							if src, ok := icon["src"].(string); ok && !strings.HasPrefix(src, "/") && !strings.Contains(src, ":") {
								icon["src"] = "/static/" + src
							}
						}
					}
				}
				if out, err := json.MarshalIndent(m, "", "  "); err == nil {
					data = out
				}
			}
			asset = newStaticAsset("manifest.json", data)
			asset.contentType = "application/manifest+json"
		})
		if asset == nil {
			http.NotFound(w, r)
			return
		}
		serveCachedAsset(w, r, asset, cacheRevalidate)
	}
}
