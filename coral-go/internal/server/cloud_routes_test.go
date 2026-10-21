package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/pkg/tunnel"
)

const cloudGolden = "testdata/cloud_routes.golden"

// concrete turns a chi pattern into a path the matcher can classify.
func concrete(route string) string {
	route = strings.TrimSuffix(route, "/*")
	if route == "/api/artifacts/{id}" {
		return "/api/artifacts/" + strings.Repeat("a", 64) // the table only admits a full content digest
	}
	if strings.HasPrefix(route, "/api/artifacts/{id}/comments") || route == "/api/artifacts/{id}/meta" { // the comments (#5176) and the metadata (#5465) of an artifact are under the same digest
		route = strings.Replace(route, "{id}", strings.Repeat("a", 64), 1)
	}
	segs := strings.Split(route, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") {
			segs[i] = "x"
		}
	}
	return strings.Join(segs, "/")
}

func shape(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

func registeredRoutes(t *testing.T) []string {
	t.Helper()
	r := newE2ERemote(t, "census", nil)
	rt, ok := r.srv.Router().(chi.Routes)
	require.True(t, ok)
	var out []string
	require.NoError(t, chi.Walk(rt, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, method+" "+route)
		return nil
	}))
	sort.Strings(out)
	return out
}

// Every route the server registers is listed here with the class the connector gives
// it. A new route is denied by default; this test fails until someone looks at the diff
// and re-runs with CLOUD_GOLDEN_UPDATE=1, so exposing a route to the hub is a reviewed act.
func TestCloudRouteCensus(t *testing.T) {
	var lines []string
	for _, rt := range registeredRoutes(t) {
		method, route, _ := strings.Cut(rt, " ")
		if !strings.HasPrefix(route, "/api/") && !strings.HasPrefix(route, "/ws/") {
			continue // never reachable through the tunnel (only /api/ and /ws/ are)
		}
		lines = append(lines, method+" "+route+" "+tunnel.Classify(method, concrete(route), "").String())
	}
	got := strings.Join(lines, "\n") + "\n"
	if os.Getenv("CLOUD_GOLDEN_UPDATE") == "1" {
		require.NoError(t, os.WriteFile(cloudGolden, []byte(got), 0o644))
	}
	want, err := os.ReadFile(cloudGolden)
	require.NoError(t, err)
	require.Equal(t, string(want), got, "route classification changed: review it, then CLOUD_GOLDEN_UPDATE=1 go test -tags 'dev fts5' ./internal/server -run CloudRouteCensus")
}

func TestCloudAllowlistHasNoDeadOrDangerousRules(t *testing.T) {
	registered := map[string]bool{}
	for _, rt := range registeredRoutes(t) {
		method, route, _ := strings.Cut(rt, " ")
		registered[method+" "+shape(route)] = true
	}
	for _, rule := range tunnel.RoutePatterns() {
		parts := strings.Fields(rule)
		method, pattern, tier := parts[0], parts[1], parts[2]
		if method == "HEAD" || tier == "never" {
			continue // explicit never rules pin routes; they need not match a registered one
		}
		require.True(t, registered[method+" "+shape(pattern)], "allowlist rule matches no registered route (typo?): %s", rule)
	}
	// Routes that must never be forwarded, whatever the table says.
	for _, never := range []string{
		"GET /api/settings", "PUT /api/settings", "GET /api/system/api-key", "POST /api/system/api-key/regenerate",
		"GET /api/system/editors", "POST /api/sessions/live/x/open-in-editor", // opens an editor here: local only (#3619, H4)
		"POST /api/webhooks", "PUT /api/system/database-security",
		"GET /api/cloud/status", "POST /api/cloud/connect", "POST /api/cloud/disconnect", "PUT /api/cloud/max-mode",
		"POST /api/board/p/tasks/7/review", "POST /api/board/p/tasks/7/approve", "POST /api/board/p/tasks/7/reject", // #3725
		"POST /api/board/remotes", "PUT /api/agent/ui/x", "DELETE /api/agent/ui/x", "POST /api/agent/artifacts", "GET /api/agent/tasks", "POST /api/workflows", "POST /api/scheduled/jobs",
	} {
		method, path, _ := strings.Cut(never, " ")
		require.Equal(t, tunnel.TierNever, tunnel.Classify(method, path, ""), never)
	}
}

// ── guard and settings routes through the real router ──────────────

func call(h http.Handler, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:8420"                     // the connector dials loopback, so Host is a loopback IP
	req.Header.Set("Sec-Fetch-Site", "same-origin") // the dashboard in a browser on this computer
	req.RemoteAddr = "127.0.0.1:50000"              // local: the auth middleware lets loopback through, as for the connector
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var tunneled = map[string]string{tunnel.ViaHeader: "1"}

func TestGuardRefusesTunneledRequestsOutsideThePolicy(t *testing.T) {
	r := newE2ERemote(t, "guard", nil)
	h := r.srv.Router()

	// Same requests locally work; through the tunnel they are refused before any handler runs.
	require.Equal(t, 200, call(h, "GET", "/api/health", "", nil).Code)
	require.Equal(t, 200, call(h, "GET", "/api/health", "", tunneled).Code)
	for _, p := range [][2]string{
		{"GET", "/api/settings"}, {"PUT", "/api/settings"}, {"GET", "/api/system/api-key"},
		{"POST", "/api/system/api-key/regenerate"}, {"GET", "/api/system/editors"}, {"POST", "/api/upload"},
		{"GET", "/api/cloud/status"}, {"POST", "/api/cloud/connect"}, {"PUT", "/api/cloud/allow-launch"},
	} {
		require.Equal(t, 403, call(h, p[0], p[1], `{}`, mergeHdr(tunneled, map[string]string{"Content-Type": "application/json"})).Code, "%v", p)
	}
	require.NotEqual(t, 403, call(h, "GET", "/api/settings", "", nil).Code, "local dashboard access must be unaffected")
	require.NotEqual(t, 403, call(h, "GET", "/api/cloud/status", "", nil).Code)
}

// #3619 (hub audit H4): open-in-editor opens an editor on this computer, and its handler
// already refused every non-local caller; it is out of the tunnel table, so even at the Launch
// mode the Guard refuses it before the handler runs.
func TestGuardRefusesOpenInEditorAtLaunch(t *testing.T) {
	r := newE2ERemote(t, "editor", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)
	rec := call(h, "POST", "/api/sessions/live/x/open-in-editor", `{"filepath":"a.txt"}`, mergeHdr(tunneled, js))
	require.Equal(t, 403, rec.Code)
	require.Contains(t, rec.Body.String(), "not available through the Coral Hub", "refused by the Guard, not the handler")
}

// #3725 (Security's review): a review verdict names its reviewer in subscriber_id, and Coral
// Hub can't prove it is that agent, so delegating, approving and rejecting are refused through
// the tunnel at every mode, before the handler runs; locally the handler answers.
func TestGuardRefusesTunneledReviewVerdicts(t *testing.T) {
	r := newE2ERemote(t, "verdicts", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)
	for _, p := range [][2]string{
		{"approve", `{"subscriber_id":"Security Reviewer"}`},
		{"reject", `{"subscriber_id":"QA Engineer","note":"no"}`},
		{"review", `{"subscriber_id":"Orchestrator","reviewers":["Security Reviewer"]}`},
	} {
		target := "/api/board/team/tasks/1/" + p[0]
		rec := call(h, "POST", target, p[1], mergeHdr(tunneled, js))
		require.Equal(t, 403, rec.Code, p[0])
		require.Contains(t, rec.Body.String(), "not available through the Coral Hub", "refused by the Guard, not the handler: %s", p[0])
		local := call(h, "POST", target, p[1], js)
		require.NotContains(t, local.Body.String(), "not available through the Coral Hub", "locally the handler decides: %s", p[0])
	}
}

func mergeHdr(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestGuardRefusesAbsoluteFileReadsOnlyWhenTunneled(t *testing.T) {
	r := newE2ERemote(t, "files", nil)
	h := r.srv.Router()
	secret := t.TempDir() + "/secret.txt"
	require.NoError(t, os.WriteFile(secret, []byte("TOPSECRET"), 0o600))
	for _, p := range []string{"file-content", "file-original", "resolve-path"} {
		for _, v := range []string{secret, "~/.ssh/id_rsa", "../../etc/passwd", "a/../../b", `C:\Windows\win.ini`} {
			rec := call(h, "GET", "/api/sessions/live/anyone/"+p+"?filepath="+url_(v), "", tunneled)
			require.Equal(t, 403, rec.Code, "%s %q", p, v)
			require.NotContains(t, rec.Body.String(), "TOPSECRET")
		}
	}
	// A project-relative reference is not blocked by the guard (the handler decides).
	require.NotEqual(t, 403, call(h, "GET", "/api/sessions/live/anyone/file-content?filepath=src/main.go", "", tunneled).Code)
}

func url_(s string) string {
	return strings.NewReplacer(" ", "%20", "\\", "%5C", "~", "%7E").Replace(s)
}

// The local maximum mode gates what the tunnel can do; only the local user can change it.
func TestGuardEnforcesTheLocalMaximumMode(t *testing.T) {
	r := newE2ERemote(t, "modes", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	send := func() int {
		return call(h, "POST", "/api/sessions/live/anyone/send", `{}`, mergeHdr(tunneled, js)).Code
	}
	launch := func() int { return call(h, "GET", "/api/filesystem/is-git?path=/tmp", "", tunneled).Code }
	setMode := func(m string, hdr map[string]string) int {
		return call(h, "PUT", "/api/cloud/max-mode", `{"mode":"`+m+`"}`, hdr).Code
	}

	// Default is view: reads pass, interact and launch are refused before any handler runs.
	require.Equal(t, 200, call(h, "GET", "/api/health", "", tunneled).Code)
	require.Equal(t, 403, send())
	require.Equal(t, 403, launch())

	// The tunnel cannot raise the mode, by any route.
	require.Equal(t, 403, setMode("launch", mergeHdr(tunneled, js)))
	require.Equal(t, 403, call(h, "PUT", "/api/settings", `{"cloud_max_mode":"launch"}`, mergeHdr(tunneled, js)).Code)
	require.Equal(t, 403, send())

	// The local user can.
	require.Equal(t, 200, setMode("interact", js))
	require.NotEqual(t, 403, send())
	require.Equal(t, 403, launch(), "interact does not include launch")
	require.Equal(t, 200, setMode("launch", js))
	require.NotEqual(t, 403, launch())
	require.Equal(t, 200, setMode("view", js))
	require.Equal(t, 403, send())
	require.Equal(t, 403, launch())

	// Garbage modes are refused and change nothing.
	for _, bad := range []string{"", "admin", "all", "never"} {
		require.Equal(t, 400, setMode(bad, js), bad)
	}
	require.Equal(t, 403, send())
}

func TestCloudSettingsRoutes(t *testing.T) {
	r := newE2ERemote(t, "settings", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}

	rec := call(h, "GET", "/api/cloud/status", "", nil)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `"state":"off"`, "no --cloud-url: connector off")
	// No manager attached in this test server: connecting is refused, not attempted.
	require.Equal(t, 400, call(h, "POST", "/api/cloud/connect", `{}`, js).Code)
	// State-changing calls need a JSON content type (no simple cross-site POSTs).
	require.Equal(t, 400, call(h, "POST", "/api/cloud/connect", `{}`, map[string]string{"Content-Type": "text/plain"}).Code)
	require.Equal(t, 400, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"interact"}`, map[string]string{"Content-Type": "text/plain"}).Code)
	require.Equal(t, 400, call(h, "PUT", "/api/cloud/max-mode", `{}`, js).Code)
}

// The dashboard's first-launch screen opens only when the server was started to offer it
// (#3459): a configured hub URL is not enough.
func TestCloudStatusOffersAtLaunchOnlyWithTheFlag(t *testing.T) {
	for _, tc := range []struct {
		name, url, connect string
		explicit, want     bool
	}{
		{"prod tier default URL, no flag", "https://app.coralhub.dev", "", false, false},
		{"prod tier, off", "https://app.coralhub.dev", "off", false, false},
		{"prod tier, prompt", "https://app.coralhub.dev", "prompt", false, true},
		{"prod tier, auto", "https://app.coralhub.dev", "auto", false, true},
		{"explicit URL, no flag", "https://app.coralhub.dev", "", true, true},
		{"explicit URL, off", "https://app.coralhub.dev", "off", true, false},
		{"dev, prompt", "", "prompt", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newE2ERemote(t, "offer", func(c *config.Config) {
				c.CloudURL, c.CloudConnect, c.CloudURLExplicit = tc.url, tc.connect, tc.explicit
			})
			rec := call(r.srv.Router(), "GET", "/api/cloud/status", "", nil)
			require.Equal(t, 200, rec.Code)
			var st struct {
				OfferAtLaunch *bool `json:"offer_at_launch"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
			require.NotNil(t, st.OfferAtLaunch, rec.Body.String())
			require.Equal(t, tc.want, *st.OfferAtLaunch)
		})
	}
}

const cloudExceptions = "testdata/cloud_route_exceptions.txt"

// A static route (a literal segment in the router) that the table matches through a {param}
// rule gets that rule's tier by accident: /api/board/remotes is not a board called "remotes".
// Every such case must be pinned by an explicit rule or listed, with a reason, in the
// exceptions file. The failure message prints the exact lines to review.
func TestCloudStaticSiblingsAreNotSwallowed(t *testing.T) {
	raw, err := os.ReadFile(cloudExceptions)
	require.NoError(t, err)
	allowed := map[string]bool{}
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		key, reason, ok := strings.Cut(ln, " | ")
		require.True(t, ok && strings.TrimSpace(reason) != "", "exception needs a reason: %q", ln)
		allowed[strings.TrimSpace(key)] = true
	}
	var unlisted []string
	for _, rt := range registeredRoutes(t) {
		method, route, _ := strings.Cut(rt, " ")
		if !strings.HasPrefix(route, "/api/") && !strings.HasPrefix(route, "/ws/") {
			continue
		}
		pat, tier, ok := tunnel.RuleFor(method, concrete(route))
		if !ok || tier == tunnel.TierNever {
			continue
		}
		ps, rs := strings.Split(pat, "/"), strings.Split(strings.TrimSuffix(route, "/*"), "/")
		if len(ps) != len(rs) {
			continue
		}
		for i := range ps {
			if strings.HasPrefix(ps[i], "{") && !strings.HasPrefix(rs[i], "{") {
				if k := method + " " + route + " -> " + pat; !allowed[k] {
					unlisted = append(unlisted, k+" | <reason>")
				}
				break
			}
		}
	}
	require.Empty(t, unlisted, "static routes matched through a {param} rule; pin them with a never rule or add to %s:\n%s", cloudExceptions, strings.Join(unlisted, "\n"))
}

// Only the local dashboard may touch the connector's settings: not the tunnel, not a proxy or
// script holding the API key, not a request from another machine, not a cross-site page.
func TestCloudSettingsRoutesNeedTheLocalDashboard(t *testing.T) {
	r := newE2ERemote(t, "dash", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	key := r.key()
	bearer := map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"}
	routes := [][3]string{{"GET", "/api/cloud/status", ""}, {"POST", "/api/cloud/connect", "{}"},
		{"POST", "/api/cloud/disconnect", "{}"}, {"PUT", "/api/cloud/max-mode", `{"mode":"launch"}`}}
	mk := func(method, target, body string, hdr map[string]string, remote string, fetch string) int {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Host = "127.0.0.1:8420"
		req.RemoteAddr = remote
		if fetch != "" {
			req.Header.Set("Sec-Fetch-Site", fetch)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, rt := range routes {
		m, p, b := rt[0], rt[1], rt[2]
		// The dashboard is allowed.
		require.NotEqual(t, 403, mk(m, p, b, js, "127.0.0.1:5", "same-origin"), "%s %s dashboard", m, p)
		// A script on this computer with the API key (Bearer) is not the dashboard: no extra privilege.
		require.Equal(t, 403, mk(m, p, b, bearer, "127.0.0.1:5", "same-origin"), "%s %s bearer", m, p)
		// curl without fetch metadata or Origin: not positively the dashboard.
		require.Equal(t, 403, mk(m, p, b, js, "127.0.0.1:5", ""), "%s %s curl", m, p)
		// A cross-site page in the user's browser.
		require.Equal(t, 403, mk(m, p, b, js, "127.0.0.1:5", "cross-site"), "%s %s cross-site", m, p)
		// Another machine, even with the API key.
		require.Equal(t, 403, mk(m, p, b, bearer, "203.0.113.9:5", "same-origin"), "%s %s remote", m, p)
		// The tunnel.
		require.Equal(t, 403, mk(m, p, b, mergeHdr(js, tunneled), "127.0.0.1:5", "same-origin"), "%s %s tunnel", m, p)
	}
}
