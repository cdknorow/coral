package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3511 (Security's #3509 finding 1): a browser write from any page but Coral's own is
// refused, whatever the mode: another port of localhost (same site, so the sign-in
// cookie goes with it), any other site, a sandboxed page. Coral's own page, clients
// with no browser evidence, and the Coral Hub connector's stamped requests pass.
func TestRefuseForeignBrowserWrites(t *testing.T) {
	reached := 0
	h := RefuseForeignBrowserWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	do := func(method string, headers map[string]string) int {
		r := httptest.NewRequest(method, "http://localhost:8420/api/settings", strings.NewReader(`{"cli_path_claude":"/tmp/x"}`))
		r.Host = "localhost:8420"
		r.Header.Set("Content-Type", "text/plain") // a "simple" request: no preflight
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for name, c := range map[string]struct {
		method  string
		headers map[string]string
		want    int
	}{
		"another localhost port":            {"PUT", map[string]string{"Origin": "http://localhost:3000", "Sec-Fetch-Site": "same-site"}, 403},
		"127.0.0.1 on another port":         {"POST", map[string]string{"Origin": "http://127.0.0.1:5173"}, 403},
		"another site":                      {"POST", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, 403},
		"a sandboxed page (Origin null)":    {"DELETE", map[string]string{"Origin": "null"}, 403},
		"fetch metadata only, same-site":    {"PATCH", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		"same origin but cross-site fetch":  {"PUT", map[string]string{"Origin": "http://localhost:8420", "Sec-Fetch-Site": "cross-site"}, 403},
		"a forged Coral Hub stamp":          {"PUT", map[string]string{"Origin": "http://localhost:3000", tunnel.ViaHeader: "1"}, 403},
		"Coral's own page":                  {"PUT", map[string]string{"Origin": "http://localhost:8420", "Sec-Fetch-Site": "same-origin"}, 200},
		"Coral's own page, no metadata":     {"POST", map[string]string{"Origin": "http://localhost:8420"}, 200},
		"no browser evidence (curl, agent)": {"PUT", nil, 200},
		"the user's own navigation":         {"POST", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		"a suffix host":                     {"PUT", map[string]string{"Origin": "http://localhost:8420.evil.test"}, 403},
		"a prefix host":                     {"PUT", map[string]string{"Origin": "http://localhost.evil.test:8420"}, 403},
		"127.0.0.1 on Coral's own port":     {"PUT", map[string]string{"Origin": "http://127.0.0.1:8420", "Sec-Fetch-Site": "same-site"}, 403},
		"Coral's port, no port in Origin":   {"PUT", map[string]string{"Origin": "http://localhost"}, 403},
		"a custom scheme (app webview)":     {"PUT", map[string]string{"Origin": "coral://localhost:8420"}, 403},
		"Origin with userinfo":              {"PUT", map[string]string{"Origin": "http://x@localhost:8420"}, 403},
		"foreign Origin, no fetch metadata": {"PUT", map[string]string{"Origin": "https://evil.example"}, 403},
		"Coral's host in another case":      {"PUT", map[string]string{"Origin": "http://LOCALHOST:8420", "Sec-Fetch-Site": "same-origin"}, 200},
		"the Coral Hub connector":           {"PUT", map[string]string{"Origin": "https://app.coralhub.dev", tunnel.ViaHeader: cloud.ViaSecret()}, 200},
		// #4032 (Security's #4031 finding 2): reads of /api and /ws from another page are refused too.
		"a read from another port":          {"GET", map[string]string{"Origin": "http://localhost:3000", "Sec-Fetch-Site": "same-site"}, 403},
		"a read from another site":          {"GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		"a HEAD from another port":          {"HEAD", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		"Coral's own page reads":            {"GET", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		"the user's own navigation, a read": {"GET", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		"a read with no metadata (curl)":    {"GET", nil, 200},
		"Coral Hub's connector reads":       {"GET", map[string]string{"Sec-Fetch-Site": "cross-site", tunnel.ViaHeader: cloud.ViaSecret()}, 200},
		"a forged stamp's read":             {"GET", map[string]string{"Sec-Fetch-Site": "same-site", tunnel.ViaHeader: "1"}, 403},
	} {
		reached = 0
		got := do(c.method, c.headers)
		assert.Equal(t, c.want, got, name)
		if c.want == 403 {
			assert.Zero(t, reached, "%s: the handler never runs", name)
		}
	}
}

// #3511 follow-up: behind a reverse proxy that rewrites Host to Coral's address, the
// trusted proxy's X-Forwarded-Host (forwardedHost) is Coral's own host too; nothing else is.
func TestForeignBrowserWritesBehindAProxy(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	do := func(h http.Handler, origin string) int {
		r := httptest.NewRequest("PUT", "http://localhost:8420/api/settings", strings.NewReader(`{}`))
		r.Host = "localhost:8420" // what nginx's proxy_pass sends without proxy_set_header Host
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	proxied := ForeignBrowserWrites(func(*http.Request) string { return "coral.example" })(ok)
	assert.Equal(t, http.StatusOK, do(proxied, "https://coral.example"), "the dashboard behind the proxy")
	assert.Equal(t, http.StatusOK, do(proxied, "http://localhost:8420"), "Coral's own host still counts")
	assert.Equal(t, http.StatusForbidden, do(proxied, "https://coral.example.evil.test"), "no suffix match")
	assert.Equal(t, http.StatusForbidden, do(proxied, "https://evil.example"), "another site")
	direct := ForeignBrowserWrites(func(*http.Request) string { return "" })(ok)
	assert.Equal(t, http.StatusForbidden, do(direct, "https://coral.example"), "no trusted forwarded host: refused")
	assert.Equal(t, http.StatusForbidden, do(RefuseForeignBrowserWrites(ok), "https://coral.example"))
}

// #3524: a request with Origin or Sec-Fetch-Site repeated is refused, whatever its method
// and even when one of the values is Coral's own: a browser never sends either twice.
func TestRepeatedOriginOrFetchSiteIsRefused(t *testing.T) {
	reached := 0
	h := RefuseForeignBrowserWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
	for name, c := range map[string]struct {
		method string
		hdr    http.Header
		want   int
	}{
		"two Origins, own first":        {"POST", http.Header{"Origin": {"http://localhost:8420", "http://evil.example"}}, 403},
		"two Origins, foreign first":    {"PUT", http.Header{"Origin": {"http://evil.example", "http://localhost:8420"}}, 403},
		"two Sec-Fetch-Site":            {"POST", http.Header{"Origin": {"http://localhost:8420"}, "Sec-Fetch-Site": {"same-origin", "cross-site"}}, 403},
		"a GET (WebSocket) with two":    {"GET", http.Header{"Origin": {"http://localhost:8420", "http://evil.example"}}, 403},
		"one of each (Coral's page)":    {"POST", http.Header{"Origin": {"http://localhost:8420"}, "Sec-Fetch-Site": {"same-origin"}}, 200},
		"a GET with one foreign Origin": {"GET", http.Header{"Origin": {"http://evil.example"}}, 200},
	} {
		reached = 0
		r := httptest.NewRequest(c.method, "http://localhost:8420/api/settings", strings.NewReader(`{}`))
		r.Host = "localhost:8420"
		r.Header = c.hdr
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == 0 || w.Code == http.StatusOK {
			w.Code = http.StatusOK
		}
		assert.Equal(t, c.want, w.Code, name)
		if c.want == 403 {
			assert.Zero(t, reached, name)
			assert.Contains(t, w.Body.String(), "foreign_origin", name)
		}
	}
}

// #4032: only /api and /ws are guarded on reads; the dashboard's pages and static files load as
// before (a navigation from another page is the user's choice, and those carry no data).
func TestForeignReadsOnlyGuardTheAPI(t *testing.T) {
	h := RefuseForeignBrowserWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for path, want := range map[string]int{"/api/sessions/live": 403, "/ws/coral": 403, "/ws": 403, "/static/app.js": 200, "/": 200, "/auth": 200, "/apiary": 200} {
		r := httptest.NewRequest("GET", "http://localhost:8420"+path, nil)
		r.Header.Set("Sec-Fetch-Site", "same-site")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assert.Equal(t, want, w.Code, path)
	}
}
