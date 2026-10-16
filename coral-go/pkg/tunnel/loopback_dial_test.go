package tunnel

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4011: a dial the local server refuses (its accept queue full in a burst, on Windows) is tried
// again shortly: here the server isn't listening yet when the request starts, and comes up
// 15 ms later.
func TestLoopbackDialRetriesARefusal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	cl, base, err := NewLoopbackClient("http://" + addr)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "up") })}
	t.Cleanup(func() { srv.Close() })
	go func() {
		time.Sleep(15 * time.Millisecond)
		if l, err := net.Listen("tcp", addr); err == nil {
			_ = srv.Serve(l)
		}
	}()
	resp, err := cl.Get(base.String() + "/")
	require.NoError(t, err, "the refused dial was tried again")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "up", string(b))
}

// A server that stays down is still an error, after the retries (tens of ms, not seconds).
func TestLoopbackDialGivesUpOnADownServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	cl, base, err := NewLoopbackClient("http://" + addr)
	require.NoError(t, err)
	start := time.Now()
	_, err = cl.Get(base.String() + "/")
	require.Error(t, err)
	assert.True(t, connRefused(err), "refused: %v", err)
	assert.Less(t, time.Since(start), 2*time.Second)
}
