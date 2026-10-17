package tunnel

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4172: an event stream whose hub-side reader stops reading ends after eventStreamWriteTimeout
// and frees its slot and its local handler, as other replies do after BodyIdle. Silence between
// events is not counted (TestSilentByDesignRepliesSurviveTheIdleTimeout still passes).
func TestEventStreamWithAStalledHubReaderIsEnded(t *testing.T) {
	prev := eventStreamWriteTimeout
	eventStreamWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { eventStreamWriteTimeout = prev })

	chunk := strings.Repeat("data: "+strings.Repeat("x", 1000)+"\n\n", 32)
	var handlerDone atomic.Bool
	rig := &limitRig{t: t, release: make(chan struct{})}
	t.Cleanup(rig.free)
	rig.sess, _ = connect(t, newHub(t, fastCfg()), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" {
			w.Write([]byte("ok"))
			return
		}
		defer handlerDone.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		for r.Context().Err() == nil { // an endless stream, as fast as it is taken
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}), func(l *LocalHandler) {
		l.HubLimits = HubLimits{Global: 1, BodyIdle: 300 * time.Millisecond}
	})

	req, _ := http.NewRequest("GET", "http://x/api/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	stalled, err := rig.sess.RoundTrip(req)
	require.NoError(t, err)
	defer stalled.Body.Close()
	_, err = io.ReadFull(stalled.Body, make([]byte, 1024)) // read a little, then stop
	require.NoError(t, err)

	resp, body := rig.get("/api/small")
	assertBusy(t, resp, body, "the one slot is held by the stream")
	require.Eventually(t, func() bool {
		resp, _ := rig.get("/api/small")
		return resp.StatusCode == 200
	}, 10*time.Second, 50*time.Millisecond, "the stalled event stream's slot is freed")
	assert.Eventually(t, handlerDone.Load, 5*time.Second, 20*time.Millisecond, "the local handler ended")
}
