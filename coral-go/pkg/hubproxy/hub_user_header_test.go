package hubproxy

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/pkg/tunnel"
)

// #3377 v1.4.18, #3404: the hub forwards exactly one new header, X-Coral-Hub-User, and
// only with a board write, /api/board/{project}/... other than GET or HEAD (a chat post,
// or the Operator's task actions through Coral Hub); everything else is unchanged.
func TestAllowlistHeadersHubUser(t *testing.T) {
	mk := func() http.Header {
		h := http.Header{}
		h.Set(tunnel.HubUserHeader, "hub-user-1234abcd")
		h.Set("Accept", "application/json")
		h.Set("Cookie", "hub_session=x")
		h.Set("X-Csrf-Token", "x")
		return h
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/board/team/messages"},
		{http.MethodPost, "/api/board/team/tasks"},
		{http.MethodPatch, "/api/board/team/tasks/7/amend"},
		{http.MethodPost, "/api/board/team/tasks/7/reassign"},
		{http.MethodPost, "/api/board/team/external-messages"},
	} {
		h := mk()
		allowlistHeaders(h, c.method, c.path)
		assert.Equal(t, "hub-user-1234abcd", h.Get(tunnel.HubUserHeader), "%s %s", c.method, c.path)
		assert.Equal(t, "application/json", h.Get("Accept"))
		assert.Empty(t, h.Get("Cookie"))
		assert.Empty(t, h.Get("X-Csrf-Token"))
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/board/team/messages"},
		{http.MethodGet, "/api/system/hub-operator"},
		{http.MethodPost, "/api/sessions/live/x/send"},
		{http.MethodPost, "/api/board/messages"},
	} {
		h := mk()
		allowlistHeaders(h, c.method, c.path)
		assert.Empty(t, h.Get(tunnel.HubUserHeader), "%s %s", c.method, c.path)
	}
	// SEC-TUN-01: a non-canonical spelling is removed as well, not only the canonical one.
	for _, c := range []struct {
		method, path string
		kept         bool
	}{
		{http.MethodGet, "/api/board/team/messages", false},
		{http.MethodPost, "/api/sessions/live/x/send", false},
		{http.MethodPost, "/api/board/team/tasks", true},
	} {
		h := http.Header{"x-coral-hub-user": {"hub-user-1234abcd"}, "X-CORAL-HUB-USER": {"hub-user-5678abcd"}, "accept": {"text/plain"}}
		allowlistHeaders(h, c.method, c.path)
		_, lower := h["x-coral-hub-user"]
		_, upper := h["X-CORAL-HUB-USER"]
		assert.Equal(t, c.kept, lower, "%s %s", c.method, c.path)
		assert.Equal(t, c.kept, upper, "%s %s", c.method, c.path)
		assert.Contains(t, h, "accept")
	}
	added := 0
	for k := range forwardToConnector {
		if k == tunnel.HubUserHeader {
			added++
		}
	}
	assert.Equal(t, 1, added)
	assert.Len(t, forwardToConnector, 14, "12 before, plus exactly X-Coral-Hub-User, and Idempotency-Key (#5841)")
}

// #3489: a browser can never send X-Coral-Hub-Server (the source Coral of a relayed
// cross-team message). It isn't on the browser allowlist, so every spelling is dropped,
// on every route, external-messages included; only the hub's relay path sets it.
func TestAllowlistHeadersDropsHubServerFromBrowsers(t *testing.T) {
	for _, path := range []string{"/api/board/team/external-messages", "/api/board/team/messages", "/api/sessions/live"} {
		for _, key := range []string{"X-Coral-Hub-Server", "x-coral-hub-server", "X-CORAL-HUB-SERVER"} {
			h := http.Header{}
			h[key] = []string{"0b6c6a52-8a0e-4f4e-9d61-3f1e2a1b9c00"}
			allowlistHeaders(h, http.MethodPost, path)
			assert.Empty(t, h, "%s %s must not survive", key, path)
		}
	}
}

// #3489: a browser can never send X-Coral-Hub-Notice either.
func TestAllowlistHeadersDropsHubNoticeFromBrowsers(t *testing.T) {
	for _, key := range []string{"X-Coral-Hub-Notice", "x-coral-hub-notice"} {
		h := http.Header{}
		h[key] = []string{"1"}
		allowlistHeaders(h, http.MethodPost, "/api/board/team/hub-notice")
		assert.Empty(t, h, key)
	}
}
