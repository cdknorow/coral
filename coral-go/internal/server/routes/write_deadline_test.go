package routes

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4602: a client that asks for a large reply and never reads it does not hold the handler for ever: each write has a deadline.
func TestWriteDeadlineFreesAHandlerWhoseClientNeverReads(t *testing.T) {
	finished := make(chan error, 1)
	srv := httptest.NewServer(WriteDeadlineAfter(300 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20)
		for i := 0; i < 2048; i++ { // up to 2 GiB: more than any socket buffer holds
			if _, err := w.Write(chunk); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	})))
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	_, err = fmt.Fprintf(conn, "GET /big HTTP/1.1\r\nHost: x\r\n\r\n") // and never reads
	require.NoError(t, err)
	select {
	case err := <-finished:
		assert.Error(t, err, "the write that could not make progress failed")
	case <-time.After(10 * time.Second):
		t.Fatal("the handler is still blocked writing to a client that does not read")
	}
}

// A client that reads, slowly but steadily, is not cut: the limit is per write, not for the whole reply. And a handler that is slow to
// start is not cut either.
func TestWriteDeadlineDoesNotCutAReaderOrASlowHandler(t *testing.T) {
	srv := httptest.NewServer(WriteDeadlineAfter(400 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(700 * time.Millisecond) // computing, longer than the deadline
		for i := 0; i < 4; i++ {
			fmt.Fprintf(w, "part %d\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(300 * time.Millisecond) // silence between writes, each under the deadline
		}
	})))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	n := 0
	for sc.Scan() {
		n++
	}
	assert.Equal(t, 4, n)
}

func TestWriteDeadlineLeavesAWebSocketUpgradeAlone(t *testing.T) {
	var wrapped bool
	h := WriteDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, wrapped = w.(*deadlineWriter) }))
	req := httptest.NewRequest(http.MethodGet, "/ws/coral", nil)
	req.Header.Set("Upgrade", "websocket")
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.False(t, wrapped, "an upgrade request gets the connection's own writer")
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))
	assert.True(t, wrapped)
}
