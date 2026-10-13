package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/httputil"
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

// staticAssetHandler serves files from fsys (the embedded static tree, already
// stripped of its /static/ prefix). Directories and missing files fall through
// to next, which keeps http.FileServer's behaviour for them.
func staticAssetHandler(fsys fs.FS, next http.Handler) http.Handler {
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
			v, _ = cache.LoadOrStore(name, newStaticAsset(name, data))
		}
		serveCachedAsset(w, r, v.(*staticAsset))
	})
}

// serveCachedAsset writes a, gzipped when the client accepts it and asked for
// the whole file. http.ServeContent answers If-None-Match with 304 against the
// ETag of the representation being sent, and serves ranges and HEAD.
func serveCachedAsset(w http.ResponseWriter, r *http.Request, a *staticAsset) {
	h := w.Header()
	h.Set("Content-Type", a.contentType)
	h.Set("Cache-Control", "no-cache")
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
	serveCachedAsset(w, r, page)
	return nil
}
