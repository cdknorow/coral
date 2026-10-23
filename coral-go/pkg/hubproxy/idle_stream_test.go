package hubproxy

import (
	"bufio"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// An event stream that sends its headers at once stays open through the proxy while it is idle for
// longer than the header limit: the limit is for a machine that has not started its reply, never for
// a stream that has (#6185). A bodyless GET arms the same header timer as an upload that ended.
func TestIdleEventStreamOutlivesTheHeaderLimit(t *testing.T) {
	prev := headerTimeout
	headerTimeout = 300 * time.Millisecond
	t.Cleanup(func() { headerTimeout = prev })

	release := make(chan struct{})
	remoteDone := make(chan struct{})
	_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(remoteDone)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // headers at once, then silence
		select {
		case <-release:
			_, _ = w.Write([]byte("data: late\n\n"))
		case <-r.Context().Done():
		}
	}), nil)

	req, err := http.NewRequest(http.MethodGet, hub.URL+"/api/remote/a/api/events", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	time.Sleep(3 * headerTimeout) // sleep-ok: idle past the limit; there is no event to wait for
	select {
	case <-remoteDone:
		t.Fatal("the idle stream was ended after the header limit")
	default:
	}
	close(release)
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "data: late\n", line)
}
