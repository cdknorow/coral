package cloud

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/cdknorow/coral/pkg/tunnel"
)

func TestClassifyDefaultDeny(t *testing.T) {
	cases := []struct {
		method, path string
		want         Class
	}{
		{"GET", "/api/health", ClassRead},
		{"HEAD", "/api/health", ClassRead},
		{"GET", "/api/sessions/live", ClassRead},
		{"GET", "/api/sessions/live/agent-1/chat", ClassRead},
		{"POST", "/api/sessions/live/agent-1/send", ClassWrite},
		{"POST", "/api/sessions/launch", ClassLaunch},
		{"GET", "/api/filesystem/list", ClassLaunch},
		{"GET", "/ws/terminal/agent-1", ClassRead},
		// Dangerous or local-only: denied.
		{"GET", "/api/settings", ClassDeny},
		{"PUT", "/api/settings", ClassDeny},
		{"GET", "/api/system/api-key", ClassDeny},
		{"POST", "/api/system/api-key/regenerate", ClassDeny},
		{"GET", "/api/system/editors", ClassDeny},
		{"POST", "/api/sessions/live/a/open-in-editor", ClassDeny},
		{"PUT", "/api/sessions/live/a/file-content", ClassDeny},
		{"POST", "/api/upload", ClassDeny},
		{"POST", "/api/webhooks", ClassDeny},
		{"POST", "/api/workflows", ClassDeny},
		{"PUT", "/api/system/database-security", ClassDeny},
		{"GET", "/api/cloud/status", ClassDeny},
		{"POST", "/api/cloud/disconnect", ClassDeny},
		{"PUT", "/api/cloud/allow-launch", ClassDeny},
		{"GET", "/api/servers", ClassDeny},
		{"GET", "/api/remote/x/api/health", ClassDeny},
		{"GET", "/api/agent/ui", ClassDeny},
		// Wrong method for an allowed path.
		{"DELETE", "/api/sessions/live/a/chat", ClassDeny},
		{"POST", "/api/health", ClassDeny},
		{"TRACE", "/api/health", ClassDeny},
		// Shape tricks.
		{"GET", "/api/health/", ClassDeny},
		{"GET", "/API/health", ClassDeny},
		{"GET", "/api//health", ClassDeny},
		{"GET", "/api/sessions/live/../settings", ClassDeny},
		{"GET", "/api/sessions/live/./chat", ClassDeny},
		{"GET", "/api/sessions/live//chat", ClassDeny},
		{"GET", "/api/sessions/live/a/chat/extra", ClassDeny},
		{"GET", "/api/sessions/live/a\\b/chat", ClassDeny},
		{"GET", "/", ClassDeny},
		{"GET", "", ClassDeny},
		{"GET", "api/health", ClassDeny},
		{"GET", "http://evil.example/api/health", ClassDeny},
		{"GET", "//evil.example/api/health", ClassDeny},
	}
	for _, c := range cases {
		if got := Classify(c.method, c.path); got != c.want {
			t.Errorf("%s %q = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestLaunchIsGatedByTheLocalSwitchAtRequestTime(t *testing.T) {
	on := false
	allow := Rules(func() bool { return on })
	if allow("POST", "/api/sessions/launch") || allow("GET", "/api/filesystem/list") {
		t.Fatal("launch allowed while the switch is off")
	}
	if !allow("POST", "/api/sessions/live/a/send") {
		t.Fatal("chat must work without the launch switch")
	}
	on = true
	if !allow("POST", "/api/sessions/launch") || !allow("GET", "/api/filesystem/list") {
		t.Fatal("launch denied while the switch is on")
	}
	on = false
	if allow("POST", "/api/sessions/launch") {
		t.Fatal("switch must be read on every request")
	}
	if Rules(nil)("POST", "/api/sessions/launch") {
		t.Fatal("nil switch must mean off")
	}
}

// What the hub could send as a path: the request must die either in tunnel.ValidatePath
// (traversal, encodings, absolute URLs) or in the allowlist, never reach a denied route.
func TestHubPathsCannotReachDeniedRoutes(t *testing.T) {
	allow := Rules(func() bool { return true })
	hostile := []string{
		"/api/sessions/live/a/chat/../../../../settings",
		"/api/sessions/live/%2e%2e/settings",
		"/api/sessions/live/a/%2e%2e/%2e%2e/system/api-key",
		"/api/sessions/live/a%2f..%2f..%2fsettings",
		"/api/sessions/live/a%5c..%5csettings",
		"/api/%252e%252e/settings",
		"/api/health%00/../settings",
		"/api/sessions/live/‥/settings",
		"/api/sessions/live/%e2%80%a5/settings",
		"http://evil.example/api/health",
		"//evil.example/api/health",
		"/api/health;/../settings",
		"/api/system/api-key",
	}
	for _, p := range hostile {
		if err := tunnel.ValidatePath(p); err != nil {
			continue // rejected before the allowlist
		}
		dec, err := url.PathUnescape(p)
		if err != nil {
			continue
		}
		if allow(http.MethodGet, dec) {
			t.Errorf("hostile path %q (decoded %q) reached an allowed route", p, dec)
		}
	}
}

func TestEveryRuleIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range table {
		if r.pattern == "" || r.pattern[0] != '/' {
			t.Errorf("bad pattern %q", r.pattern)
		}
		if r.class == ClassDeny {
			t.Errorf("deny rules are implicit; remove %s %s", r.method, r.pattern)
		}
		if r.pattern != "/api/health" && len(r.pattern) > 4 && r.pattern[:5] != "/api/" && r.pattern[:4] != "/ws/" {
			t.Errorf("rule outside /api and /ws: %s", r.pattern)
		}
		k := r.method + " " + r.pattern
		if seen[k] {
			t.Errorf("duplicate rule %s", k)
		}
		seen[k] = true
	}
}
