package hubproxy

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3377 v1.4.18: the hub forwards exactly one new header, X-Coral-Hub-User, and only
// with a chat post, POST /api/board/{project}/messages; everything else is unchanged.
func TestAllowlistHeadersHubUser(t *testing.T) {
	mk := func() http.Header {
		h := http.Header{}
		h.Set(tunnel.HubUserHeader, "hub-user-1234abcd")
		h.Set("Accept", "application/json")
		h.Set("Cookie", "hub_session=x")
		h.Set("X-Csrf-Token", "x")
		return h
	}
	h := mk()
	allowlistHeaders(h, http.MethodPost, "/api/board/team/messages")
	assert.Equal(t, "hub-user-1234abcd", h.Get(tunnel.HubUserHeader))
	assert.Equal(t, "application/json", h.Get("Accept"))
	assert.Empty(t, h.Get("Cookie"))
	assert.Empty(t, h.Get("X-Csrf-Token"))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/board/team/messages"},
		{http.MethodPost, "/api/board/team/tasks"},
		{http.MethodPost, "/api/sessions/live/x/send"},
		{http.MethodPost, "/api/board/team/messages/mark-read"},
	} {
		h := mk()
		allowlistHeaders(h, c.method, c.path)
		assert.Empty(t, h.Get(tunnel.HubUserHeader), "%s %s", c.method, c.path)
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
