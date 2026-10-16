package tunnel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const goodRouteKey = "0123456789abcdef0123456789abcdef"

// routeHub is a hub that hands out routeKey in Welcome and records the RouteHeader of each dial.
type routeHub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	headers []string
	key     string
}

func newRouteHub(t *testing.T, key string) *routeHub {
	t.Helper()
	h := &routeHub{key: key}
	cfg := SessionConfig{RouteKey: func(any) string { h.mu.Lock(); defer h.mu.Unlock(); return h.key }}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.headers = append(h.headers, r.Header.Get(RouteHeader))
		h.mu.Unlock()
		s, err := AcceptHTTP(w, r, func(_ context.Context, tok string) (any, error) {
			if tok != testToken {
				return nil, ErrUnauthorized
			}
			return "server-1", nil
		}, cfg)
		if err == nil {
			t.Cleanup(func() { s.Close() })
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *routeHub) url() string { return "ws" + strings.TrimPrefix(h.srv.URL, "http") }

func (h *routeHub) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.headers...)
}

func dialRoute(t *testing.T, url string, stored *string) (*ClientSession, string) {
	t.Helper()
	var got string
	cs, err := Dial(context.Background(), url, testToken, ClientOptions{
		Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true,
		RouteKey:   func() string { return *stored },
		OnRouteKey: func(k string) { got = k },
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return cs, got
}

// #3683: the hub's route key reaches the connector in Welcome, and the connector sends it back
// as the RouteHeader on its next dial; with none stored, no header.
func TestRouteKeyRoundTrip(t *testing.T) {
	h := newRouteHub(t, goodRouteKey)
	stored := ""
	_, got := dialRoute(t, h.url(), &stored)
	assert.Equal(t, goodRouteKey, got, "Welcome carried it")
	stored = got
	dialRoute(t, h.url(), &stored)
	assert.Equal(t, []string{"", goodRouteKey}, h.seen(), "none on the first dial, the key on the next")
}

// A malformed key is never sent in Welcome by this package's hub side, never handed to the
// owner by the connector, and a malformed stored key is never sent as the header.
func TestRouteKeyInvalidValuesAreIgnored(t *testing.T) {
	bad := []string{
		strings.ToUpper(goodRouteKey), goodRouteKey[:31], goodRouteKey + "0", "g123456789abcdef0123456789abcdef",
		"0123456789abcdef 123456789abcdef", "0123456789abcdef0123456789abcde\n", "",
	}
	for _, k := range bad {
		h := newRouteHub(t, k)
		stored := k // a malformed stored key too
		_, got := dialRoute(t, h.url(), &stored)
		assert.Empty(t, got, "%q", k)
		assert.Equal(t, []string{""}, h.seen(), "%q is not sent", k)
	}

	// A hub that puts a malformed key in Welcome itself (not this package): the connector ignores it.
	raw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
		b, _ := json.Marshal(Welcome{AcceptedVersion: ProtocolVersion, RouteKey: "NOT-A-KEY"})
		_ = c.Write(ctx, websocket.MessageBinary, b)
		for { // until the connector closes (a hijacked request's context never ends)
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}))
	t.Cleanup(raw.Close)
	stored := ""
	_, got := dialRoute(t, "ws"+strings.TrimPrefix(raw.URL, "http"), &stored)
	assert.Empty(t, got)
}

func TestValidRouteKey(t *testing.T) {
	assert.True(t, ValidRouteKey(goodRouteKey))
	assert.True(t, ValidRouteKey(strings.Repeat("f", 32)))
	for _, k := range []string{"", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32), strings.Repeat("a", 31) + "-", "\u00e9" + strings.Repeat("a", 30)} {
		assert.False(t, ValidRouteKey(k), "%q", k)
	}
}
