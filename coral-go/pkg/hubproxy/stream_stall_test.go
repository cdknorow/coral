package hubproxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4172: a browser that connects to an event stream and never reads it does not hold the
// machine's stream for as long as its connection lasts: after respWriteTimeout of a write that
// cannot make progress, the proxy ends the reply and the machine's handler returns.
func TestEventStreamToABrowserThatNeverReadsIsEnded(t *testing.T) {
	prev := respWriteTimeout
	respWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { respWriteTimeout = prev })

	chunk := strings.Repeat("data: "+strings.Repeat("x", 1000)+"\n\n", 64)
	var remoteDone atomic.Bool
	_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer remoteDone.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		for r.Context().Err() == nil { // an endless stream
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}), nil)

	u, err := url.Parse(hub.URL)
	require.NoError(t, err)
	conn, err := net.Dial("tcp", u.Host)
	require.NoError(t, err)
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(2048)
	}
	_, err = fmt.Fprintf(conn, "GET /api/remote/a/api/events HTTP/1.1\r\nHost: %s\r\nAccept: text/event-stream\r\n\r\n", u.Host)
	require.NoError(t, err)
	// ... and never read.
	assert.Eventually(t, remoteDone.Load, 15*time.Second, 50*time.Millisecond, "the machine's handler is still running for a browser that never reads")
}
