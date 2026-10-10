package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
)

const cloudGolden = "testdata/cloud_routes.golden"

// concrete turns a chi pattern into a path the matcher can classify.
func concrete(route string) string {
	route = strings.TrimSuffix(route, "/*")
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
		method, pattern := parts[0], parts[1]
		if method == "HEAD" {
			continue
		}
		require.True(t, registered[method+" "+shape(pattern)], "allowlist rule matches no registered route (typo?): %s", rule)
	}
	require.Equal(t, tunnel.TierLaunch, tunnel.Classify("POST", "/api/sessions/live/x/open-in-editor", ""), "spawns a process on the host: launch tier")
	// Routes that must never be forwarded, whatever the table says.
	for _, never := range []string{
		"GET /api/settings", "PUT /api/settings", "GET /api/system/api-key", "POST /api/system/api-key/regenerate",
		"GET /api/system/editors",
		"POST /api/webhooks", "PUT /api/system/database-security", "POST /api/license/activate",
		"GET /api/cloud/status", "POST /api/cloud/connect", "POST /api/cloud/disconnect", "PUT /api/cloud/max-mode",
		"POST /api/board/remotes", "GET /api/agent/ui", "POST /api/workflows", "POST /api/scheduled/jobs",
	} {
		method, path, _ := strings.Cut(never, " ")
		require.Equal(t, tunnel.TierNever, tunnel.Classify(method, path, ""), never)
	}
}

// ── guard and settings routes through the real router ──────────────

func call(h http.Handler, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:8420"        // the connector dials loopback, so Host is a loopback IP
	req.RemoteAddr = "127.0.0.1:50000" // local: the auth middleware lets loopback through, as for the connector
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var tunneled = map[string]string{cloud.TunnelHeader: "1"}

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
