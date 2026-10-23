package testrig

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// FakeGitHub serves releases the way github.com and its API do for the updater: the list of the newest releases at /repos/<repo>/releases (newest first), a product release's files and a
// promotion release's (promote-<tag>-<sequence>: promote.json and its bundle, #6393) at /<repo>/releases/download/<tag>/<file>, and the old /<repo>/releases/latest/download/<file>
// (which the client no longer reads). It listens on loopback only. A test can make any file wrong with Mutate, Status or RedirectTo; the list's key is "list".
type FakeGitHub struct {
	srv, cdn *httptest.Server
	mu       sync.Mutex
	releases map[string]*Release      // by tag
	promos   map[string]*promoRelease // by promotion release tag
	order    []string                 // tags of every release, products and promotions, in the order they were published
	// ListBody, when set, is what the list answers instead of the real one (a test's junk or oversize list).
	ListBody []byte
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
	g := &FakeGitHub{releases: map[string]*Release{}, promos: map[string]*promoRelease{}, Status: map[string]int{}, RedirectTo: map[string]string{}, Hits: map[string]int{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	g.cdn = httptest.NewServer(http.HandlerFunc(g.serve))
	return g
}

// Close stops both listeners.
func (g *FakeGitHub) Close() { g.srv.Close(); g.cdn.Close() }

// promoRelease is a promotion release: the two files and whether the listing says it has them.
type promoRelease struct {
	manifest, bundle []byte
	draft            bool
	noAssets         bool
}

// PromotionTag is the tag of the promotion release of product tag at sequence.
func PromotionTag(tag string, sequence int64) string {
	return fmt.Sprintf("promote-%s-%d", tag, sequence)
}

// Publish adds a product release and, as the promote workflow does, its promotion release promote-<tag>-<sequence> carrying the manifest and its bundle; it is the latest when latest is true.
func (g *FakeGitHub) Publish(r *Release, latest bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.releases[r.Tag] = r
	g.order = append(g.order, r.Tag)
	pt := PromotionTag(r.Tag, r.Sequence)
	g.promos[pt] = &promoRelease{manifest: r.Manifest, bundle: r.ManifestBundle}
	g.order = append(g.order, pt)
	if latest {
		g.latest = r.Tag
	}
}

// PublishPromotion adds a promotion release by hand (junk, a replay, a manifest for another tag): the tag is promote-<tag>-<sequence> exactly as given, with the files given.
func (g *FakeGitHub) PublishPromotion(releaseTag string, manifest, bundle []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.promos[releaseTag] = &promoRelease{manifest: manifest, bundle: bundle}
	g.order = append(g.order, releaseTag)
}

// PublishDraftPromotion adds a promotion release that is a draft (the API lists drafts only to someone who may see them; the client must never take one).
func (g *FakeGitHub) PublishDraftPromotion(releaseTag string, manifest, bundle []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.promos[releaseTag] = &promoRelease{manifest: manifest, bundle: bundle, draft: true}
	g.order = append(g.order, releaseTag)
}

// PublishPromotionWithoutAssets adds a promotion release that lists no assets (the listing says so, and a download of its files answers 404).
func (g *FakeGitHub) PublishPromotionWithoutAssets(releaseTag string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.promos[releaseTag] = &promoRelease{noAssets: true}
	g.order = append(g.order, releaseTag)
}

// listJSON is what the API's list of releases answers: newest first.
func (g *FakeGitHub) listJSON() []byte {
	type asset struct {
		Name string `json:"name"`
	}
	type rel struct {
		TagName    string  `json:"tag_name"`
		Draft      bool    `json:"draft"`
		Prerelease bool    `json:"prerelease"`
		Assets     []asset `json:"assets"`
	}
	var out []rel
	for i := len(g.order) - 1; i >= 0; i-- {
		tag := g.order[i]
		if p, ok := g.promos[tag]; ok {
			r := rel{TagName: tag, Draft: p.draft, Prerelease: true}
			if !p.noAssets {
				r.Assets = []asset{{stage.ManifestFile}, {stage.ManifestBundleFile}}
			}
			out = append(out, r)
			continue
		}
		out = append(out, rel{TagName: tag, Assets: []asset{{stage.SumsFile}, {stage.SumsBundleFile}}})
	}
	b, _ := json.Marshal(out)
	return b
}

// URL is the address of the server.
func (g *FakeGitHub) URL() string { return g.srv.URL }

// Config is what the updater's stage.Config needs to use this server instead of github.com: the base URLs, and a fetcher that allows its host over http.
func (g *FakeGitHub) Config(c *stage.Config) {
	c.ReleasesURL = g.srv.URL + "/repos/" + FakeRepo + "/releases?per_page=30"
	c.DownloadBase = g.srv.URL + "/" + FakeRepo + "/releases/download/"
	c.Fetcher = &verify.Fetcher{Scheme: "http", Allow: func(host string) bool { return host == "127.0.0.1" }}
}

func (g *FakeGitHub) file(tag, name string) ([]byte, bool) {
	if p, ok := g.promos[tag]; ok {
		switch {
		case p.noAssets:
			return nil, false
		case name == stage.ManifestFile:
			return p.manifest, true
		case name == stage.ManifestBundleFile:
			return p.bundle, true
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
	case name == r.AssetName, r.AssetName == Asset(r.Version) && name == "coral-linux-amd64.tar.gz":
		return r.Package, true
	}
	return nil, false
}

func (g *FakeGitHub) serve(w http.ResponseWriter, req *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := strings.TrimPrefix(req.URL.Path, "/")
	g.Hits[p]++
	if p == "repos/"+FakeRepo+"/releases" {
		if code, ok := g.Status["list"]; ok {
			w.WriteHeader(code)
			return
		}
		body := g.listJSON()
		if g.ListBody != nil {
			body = g.ListBody
		}
		if g.Mutate != nil {
			body = g.Mutate("list", append([]byte(nil), body...))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		return
	}
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
