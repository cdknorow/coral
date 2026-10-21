package hubproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/tunnel"
)

// #3410: a malicious connector writes its response header frame itself, so header names
// arrive as it spells them. A lowercase set-cookie, access-control-* or
// content-security-policy must be filtered like the canonical spelling: the reverse
// proxy would otherwise write them out canonicalized on the hub origin.
func TestNonCanonicalConnectorResponseHeadersAreFiltered(t *testing.T) {
	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{})
		if err == nil {
			sessions <- s
		}
	}))
	t.Cleanup(ths.Close)
	evil := tunnel.HandlerFunc(func(ctx context.Context, s *tunnel.Stream) {
		w, err := s.Respond(tunnel.ResponseHeader{Status: 200, Headers: http.Header{
			"set-cookie":                       {"hub_session=stolen; Path=/"},
			"SET-COOKIE":                       {"other=1"},
			"access-control-allow-origin":      {"*"},
			"access-control-allow-credentials": {"true"},
			"content-security-policy":          {"script-src *"},
			"content-type":                     {"text/html"},
			"refresh":                          {"0; url=https://evil.example"},
		}})
		if err == nil {
			io.WriteString(w, "<script>alert(1)</script>")
			w.Close()
		}
	})
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{Handler: evil, AllowInsecure: true})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	sess := <-sessions
	t.Cleanup(func() { sess.Close() })
	hub := cloudHub(t, tunnelResolver{sess}, nil)

	resp, err := http.Get(hub.URL + "/api/remote/srv/api/x")
	require.NoError(t, err)
	defer resp.Body.Close()
	for k := range resp.Header {
		c := http.CanonicalHeaderKey(k)
		assert.NotEqual(t, "Set-Cookie", c)
		assert.NotEqual(t, "Refresh", c)
		assert.False(t, strings.HasPrefix(c, "Access-Control-"), k)
	}
	assert.Equal(t, []string{"sandbox; default-src 'none'"}, resp.Header.Values("Content-Security-Policy"))
	assert.NotContains(t, resp.Header.Get("Content-Type"), "text/html")
}

// SEC-TUN-03: the filter deletes Access-Control-* (and everything else it strips) whatever
// the spelling of the name, also when given a header that pkg/tunnel did not canonicalize.
func TestStrictResponseFilterNonCanonicalNames(t *testing.T) {
	resp := &http.Response{Header: http.Header{
		"access-control-allow-origin": {"*"},
		"Access-Control-Max-Age":      {"1"},
		"set-cookie":                  {"a=1"},
		"x-frame-options":             {"ALLOWALL"},
		"content-type":                {"text/plain"},
	}}
	require.NoError(t, StrictResponseFilter(resp))
	for k := range resp.Header {
		c := http.CanonicalHeaderKey(k)
		assert.Equal(t, c, k, "names are canonical after the filter")
		assert.False(t, strings.HasPrefix(c, "Access-Control-"), k)
		assert.NotEqual(t, "Set-Cookie", c)
		assert.NotEqual(t, "X-Frame-Options", c)
	}
	assert.Equal(t, "text/plain", resp.Header.Get("Content-Type"), "found under its lowercase name")
}

// SEC-TUN-04: a WebSocket through the hub is only for /ws/ paths.
func TestWebSocketOnlyForWSPaths(t *testing.T) {
	assert.True(t, wsPathAllowed("/ws/terminal/a"))
	assert.True(t, wsPathAllowed("/ws/coral"))
	assert.False(t, wsPathAllowed("/api/board/p/messages"))
	assert.False(t, wsPathAllowed("/api/sessions/live"))
	assert.True(t, httpPathAllowed("/api/board/p/messages"))
	assert.False(t, httpPathAllowed("/ws/coral"))
}
