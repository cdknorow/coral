package testrig

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// FakeGitHub serves releases the way github.com does for the updater: /<repo>/releases/download/<tag>/<file>, redirecting to a second address as
// GitHub does, and /<repo>/releases/latest/download/<file> for the product's stable names. A product release (v<version>) has the sums, their
// bundle and the package; a promotion is a release of its own (#326): promote-<sequence> with promote.json and its bundle, and promote-v<version>
// with the first promotion of that version. It listens on loopback only. A test can make any file wrong with Mutate, Status or RedirectTo.
type FakeGitHub struct {
	srv, cdn   *httptest.Server
	mu         sync.Mutex
	releases   map[string]*Release  // product releases, by tag
	promotions map[int64]promotion  // promote-<sequence>
	real       map[int64]bool       // the sequences Publish promoted (PublishPromotion's are anyone's)
	firsts     map[string]promotion // promote-v<version>
	latest     string
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

// promotion is a promote release's two files.
type promotion struct{ manifest, bundle []byte }

// NewFakeGitHub starts the server; Close stops it.
func NewFakeGitHub() *FakeGitHub {
	g := &FakeGitHub{releases: map[string]*Release{}, promotions: map[int64]promotion{}, real: map[int64]bool{}, firsts: map[string]promotion{}, Status: map[string]int{},
		RedirectTo: map[string]string{}, Hits: map[string]int{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	g.cdn = httptest.NewServer(http.HandlerFunc(g.serve))
	return g
}

// Close stops both listeners.
func (g *FakeGitHub) Close() { g.srv.Close(); g.cdn.Close() }

// Publish adds a product release and the first promotion of its version (promote-v<version>, kept for the rollback). With latest it is also
// promoted now: promote-<r.Sequence>, and the product's latest release.
func (g *FakeGitHub) Publish(r *Release, latest bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.releases[r.Tag] = r
	if _, ok := g.firsts[r.Version]; !ok {
		g.firsts[r.Version] = promotion{r.Manifest, r.ManifestBundle}
	}
	if latest {
		g.latest = r.Tag
		g.promotions[r.Sequence] = promotion{r.Manifest, r.ManifestBundle}
		g.real[r.Sequence] = true
	}
}

// PublishPromotion publishes promote-<seq> with these two files, whatever they are (a test's junk or forged promotion).
func (g *FakeGitHub) PublishPromotion(seq int64, manifest, bundle []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.promotions[seq] = promotion{manifest, bundle}
}

// Taken is every promote-<n> published, real or not, ascending: what the promote workflow reads from the tags.
func (g *FakeGitHub) Taken() []int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []int64
	for n := range g.promotions {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// URL is the address of the server.
func (g *FakeGitHub) URL() string { return g.srv.URL }

// Config is what the updater's stage.Config needs to use this server instead of github.com: the base URL, and a fetcher that allows its host over http.
func (g *FakeGitHub) Config(c *stage.Config) {
	c.DownloadBase = g.srv.URL + "/" + FakeRepo + "/releases/download/"
	c.Fetcher = &verify.Fetcher{Scheme: "http", Allow: func(host string) bool { return host == "127.0.0.1" }}
}

func (g *FakeGitHub) file(tag, name string) ([]byte, bool) {
	if p, ok := g.promotionFor(tag); ok {
		switch name {
		case stage.ManifestFile:
			return p.manifest, p.manifest != nil
		case stage.ManifestBundleFile:
			return p.bundle, p.bundle != nil
		}
		return nil, false
	}
	r, ok := g.releases[tag]
	if !ok {
		return nil, false
	}
	switch {
	case name == stage.SumsFile:
		return r.Sums, true
	case name == stage.SumsBundleFile:
		return r.SumsBundle, true
	case name == Asset(r.Version), name == "coral-linux-amd64.tar.gz":
		return r.Package, true
	}
	return nil, false
}

// promotionFor is the promotion a promote-<sequence> or promote-v<version> tag names.
func (g *FakeGitHub) promotionFor(tag string) (promotion, bool) {
	if v, ok := strings.CutPrefix(tag, stage.RollbackPrefix); ok {
		p, ok := g.firsts[v]
		return p, ok
	}
	if n, ok := strings.CutPrefix(tag, stage.PromotionPrefix); ok {
		seq, err := strconv.ParseInt(n, 10, 64)
		if err != nil || seq < 1 {
			return promotion{}, false
		}
		if p, ok := g.promotions[seq]; ok {
			return p, true
		}
		// The workflow numbers each promotion above every promote-<n> that exists, so below the highest real promotion every name exists; a test
		// that publishes only sequence 7 gets 1 to 6 as well, each carrying the next real promotion's files (a sequence that isn't its own: the
		// updater skips them). Junk published far above leaves a gap, as it would on GitHub.
		var next int64
		for s := range g.real {
			if s > seq && (next == 0 || s < next) {
				next = s
			}
		}
		if next == 0 {
			return promotion{}, false
		}
		return g.promotions[next], true
	}
	return promotion{}, false
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
