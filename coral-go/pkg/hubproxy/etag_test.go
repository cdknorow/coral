package hubproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3439: a poll's validator works through the hub as it is pinned today. The hub drops ETag (and
// every caching header, and sets no-store), so Coral sends the same validator in
// X-Coral-Validator: that reaches the browser, the browser's If-None-Match reaches the machine,
// and the machine's 304 comes back as a 304 with no body. No hub change is needed.
func TestConditionalPollThroughTheHub(t *testing.T) {
	const etag = `W/"abc123"`
	var gotINM []string
	local := http.NewServeMux()
	local.HandleFunc("/api/sessions/live/x/poll", func(w http.ResponseWriter, r *http.Request) {
		gotINM = append(gotINM, r.Header.Get("If-None-Match"))
		w.Header().Set("ETag", etag)
		w.Header().Set("X-Coral-Validator", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"capture":{"capture":"$ "}}`)
	})
	ls := httptest.NewServer(local)
	defer ls.Close()
	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{})
		if err == nil {
			sessions <- s
		}
	}))
	defer ths.Close()
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler: &tunnel.LocalHandler{BaseURL: ls.URL, AllowPath: func(string, string) bool { return true },
			MaxMode: func() tunnel.Mode { return tunnel.TierView }},
		AllowInsecure: true,
	})
	require.NoError(t, err)
	defer cs.Close()
	var sess *tunnel.Session
	select {
	case sess = <-sessions:
	case <-time.After(5 * time.Second):
		t.Fatal("no tunnel session")
	}
	defer sess.Close()

	h := NewWithOptions(tunnelResolver{sess}, Options{AllowedOrigins: []string{"hub.test"}})
	r := chi.NewRouter()
	r.Handle("/api/remote/{server}/*", h)
	hub := httptest.NewServer(r)
	defer hub.Close()

	get := func(inm string) (int, string, string) {
		req, _ := http.NewRequest(http.MethodGet, hub.URL+"/api/remote/srv/api/sessions/live/x/poll", nil)
		req.Header.Set("Origin", "http://hub.test")
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		assert.Empty(t, resp.Header.Get("ETag"), "the hub drops ETag, as designed")
		assert.Equal(t, "private, no-store", resp.Header.Get("Cache-Control"), "and never lets the browser cache")
		return resp.StatusCode, resp.Header.Get("X-Coral-Validator"), string(b)
	}
	code, e, body := get("")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, etag, e, "the machine's validator reaches the browser")
	assert.Contains(t, body, `"capture"`)

	code, e, body = get(etag)
	assert.Equal(t, http.StatusNotModified, code, "the machine's 304 comes back as a 304")
	assert.Empty(t, body, "with no body")
	assert.Equal(t, etag, e)
	assert.Equal(t, []string{"", etag}, gotINM, "If-None-Match reaches the machine")
}
