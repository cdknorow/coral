package testrig

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// FakeGitHub serves releases the way github.com does for the updater: /<repo>/releases/latest/download/<file> (redirecting to the newest release's file, as
// GitHub does, to a second address) and /<repo>/releases/download/<tag>/<file>. It listens on loopback only. A test can make any file wrong with Mutate,
// Status or RedirectTo.
type FakeGitHub struct {
	srv, cdn *httptest.Server
	mu       sync.Mutex
	releases map[string]*Release // by tag
	latest   string
	// Mutate may change a file's bytes on its way out (the path is "<tag>/<file>" or "latest/<file>"); nil leaves them.
	Mutate func(path string, body []byte) []byte
	// Status answers a path with this status and no body.
	Status map[string]int
	// RedirectTo sends a path to this absolute URL instead of its file.
	RedirectTo map[string]string
	// NoRedirect serves the latest files directly.
	NoRedirect bool
	// Hits counts requests by path.
	Hits map[string]int
}

// NewFakeGitHub starts the server; Close stops it.
func NewFakeGitHub() *FakeGitHub {
	g := &FakeGitHub{releases: map[string]*Release{}, Status: map[string]int{}, RedirectTo: map[string]string{}, Hits: map[string]int{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	g.cdn = httptest.NewServer(http.HandlerFunc(g.serve))
	return g
}

// Close stops both listeners.
func (g *FakeGitHub) Close() { g.srv.Close(); g.cdn.Close() }

// Publish adds a release; it is the latest when latest is true.
func (g *FakeGitHub) Publish(r *Release, latest bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.releases[r.Tag] = r
	if latest {
		g.latest = r.Tag
	}
}

// URL is the address of the server.
func (g *FakeGitHub) URL() string { return g.srv.URL }

// Config is what the updater's stage.Config needs to use this server instead of github.com: the base URLs, and a fetcher that allows its host over http.
func (g *FakeGitHub) Config(c *stage.Config) {
	c.LatestBase = g.srv.URL + "/" + FakeRepo + "/releases/latest/download/"
	c.DownloadBase = g.srv.URL + "/" + FakeRepo + "/releases/download/"
	c.Fetcher = &verify.Fetcher{Scheme: "http", Allow: func(host string) bool { return host == "127.0.0.1" }}
}

func (g *FakeGitHub) file(tag, name string) ([]byte, bool) {
	r, ok := g.releases[tag]
	if !ok {
		return nil, false
	}
	switch {
	case name == stage.ManifestFile:
		return r.Manifest, true
	case name == stage.ManifestBundleFile:
		return r.ManifestBundle, true
	case name == stage.SumsFile:
		return r.Sums, true
	case name == stage.SumsBundleFile:
		return r.SumsBundle, true
	case name == Asset(r.Version), name == "coral-linux-amd64.tar.gz":
		return r.Package, true
	}
	return nil, false
}

// Requests counts the requests so far whose path ends with suffix (the package's file name, to say a test served it).
func (g *FakeGitHub) Requests(suffix string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for p, c := range g.Hits {
		if strings.HasSuffix(p, suffix) {
			n += c
		}
	}
	return n
}

func (g *FakeGitHub) serve(w http.ResponseWriter, req *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := strings.TrimPrefix(req.URL.Path, "/")
	g.Hits[p]++
	pre := FakeRepo + "/releases/"
	rest, ok := strings.CutPrefix(p, pre)
	if !ok {
		http.NotFound(w, req)
		return
	}
	var tag, name, key string
	if r2, ok := strings.CutPrefix(rest, "latest/download/"); ok {
		tag, name, key = g.latest, r2, "latest/"+r2
	} else if r2, ok := strings.CutPrefix(rest, "download/"); ok {
		i := strings.Index(r2, "/")
		if i < 0 {
			http.NotFound(w, req)
			return
		}
		tag, name, key = r2[:i], r2[i+1:], r2
	} else {
		http.NotFound(w, req)
		return
	}
	if to, ok := g.RedirectTo[key]; ok {
		http.Redirect(w, req, to, http.StatusFound)
		return
	}
	if code, ok := g.Status[key]; ok {
		w.WriteHeader(code)
		return
	}
	body, ok := g.file(tag, name)
	if !ok {
		http.NotFound(w, req)
		return
	}
	if strings.HasPrefix(key, "latest/") && !g.NoRedirect && req.Host == strings.TrimPrefix(g.srv.URL, "http://") {
		// GitHub answers latest/download with a redirect to the file of the newest release, served from another address
		http.Redirect(w, req, g.cdn.URL+"/"+FakeRepo+"/releases/download/"+tag+"/"+name, http.StatusFound)
		return
	}
	if g.Mutate != nil {
		body = g.Mutate(key, append([]byte(nil), body...))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(body)
}
