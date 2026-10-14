package tunnel

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEC-TUN-04 (#3410): the connector opens a WebSocket stream only for a /ws/ path, as it
// sends an HTTP stream only for an /api/ path; the local API is never reached otherwise.
func TestLocalHandlerWebSocketOnlyForWSPaths(t *testing.T) {
	var reached atomic.Int32
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.Write([]byte("ok"))
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.AllowRequest = AllowRequest(func() Mode { return TierLaunch })
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, path := range []string{"/api/board/p/messages", "/api/sessions/live", "/api/health"} {
		require.Equal(t, TierView, Classify(http.MethodGet, path, ""), "a route the table allows over HTTP")
		hs, err := sess.OpenStream(ctx, Header{Kind: KindWS, Method: http.MethodGet, Path: path, Headers: http.Header{}})
		require.NoError(t, err, path)
		_, err = sess.readResponse(hs)
		var se *StreamError
		require.True(t, errors.As(err, &se), "%s: %v", path, err)
		hs.Close()
	}
	assert.Zero(t, reached.Load(), "the local API was never reached")
}

// #3410: header names from the far side are made canonical; names that differ only in
// case are merged.
func TestCanonicalHeader(t *testing.T) {
	assert.Nil(t, CanonicalHeader(nil))
	got := CanonicalHeader(http.Header{"set-cookie": {"a=1"}, "Set-Cookie": {"b=2"}, "x-thing": {"1"}})
	assert.ElementsMatch(t, []string{"a=1", "b=2"}, got["Set-Cookie"])
	assert.Equal(t, []string{"1"}, got["X-Thing"])
	assert.Len(t, got, 2)
}
