package hubproxy

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/pkg/tunnel"
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
	assert.Len(t, forwardToConnector, 13, "12 before, plus exactly X-Coral-Hub-User")
}
