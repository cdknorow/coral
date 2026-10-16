package tunnel

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// headerV149 is the stream header as a v1.4.49 connector (before #3642) declares it.
type headerV149 struct {
	Kind          Kind        `json:"kind"`
	Method        string      `json:"method,omitempty"`
	Path          string      `json:"path"`
	Query         string      `json:"query,omitempty"`
	Headers       http.Header `json:"headers,omitempty"`
	Protocols     []string    `json:"protocols,omitempty"`
	ContentLength int64       `json:"content_length,omitempty"`
}

// coral-hub's question on #3642: an older connector ignores hub_user, it doesn't refuse the
// stream. v1.4.49's decodeHeader is json.Unmarshal into this shape (no DisallowUnknownFields),
// then validate; and today's decoder ignores a field it doesn't know the same way.
func TestOlderConnectorIgnoresHubUser(t *testing.T) {
	b, err := json.Marshal(Header{Kind: KindHTTP, Method: "GET", Path: "/api/sessions/live", HubUser: "hub-user-0123abcd"})
	require.NoError(t, err)
	require.Contains(t, string(b), `"hub_user":"hub-user-0123abcd"`)
	var old headerV149
	require.NoError(t, json.Unmarshal(b, &old), "the v1.4.49 shape decodes a header that carries hub_user")
	assert.Equal(t, "/api/sessions/live", old.Path)

	future := []byte(`{"kind":"http","method":"GET","path":"/api/x","a_later_field":{"n":1}}`)
	h, err := decodeHeader(future, Limits{})
	require.NoError(t, err, "a field this connector doesn't know is ignored")
	assert.Equal(t, "/api/x", h.Path)
}

// Routes that are silent by design are never cut by the body idle timeout (coral-hub's
// question on #3642; times scaled: BodyIdle 400 ms stands for 60 s):
//   - a long poll waits before its headers, and the timer starts only once they are in;
//   - a reply that pauses for less than BodyIdle (the "45 s of 60 s" gap) completes;
//   - an event stream may stay silent for longer than BodyIdle.
func TestSilentByDesignRepliesSurviveTheIdleTimeout(t *testing.T) {
	const idle = 400 * time.Millisecond
	rig := &limitRig{t: t, release: make(chan struct{})}
	rig.sess, _ = connect(t, newHub(t, fastCfg()), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/board/p/waits/poll":
			time.Sleep(3 * idle) // the poll waits, then answers
			w.Write([]byte("woke"))
		case "/api/pause":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("before "))
			w.(http.Flusher).Flush()
			time.Sleep(idle * 3 / 4)
			w.Write([]byte("after"))
		case "/api/events":
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Write([]byte("data: 1\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(3 * idle)
			w.Write([]byte("data: 2\n\n"))
		}
	}), func(l *LocalHandler) {
		l.HubLimits = HubLimits{BodyIdle: idle}
	})
	for path, want := range map[string]string{
		"/api/board/p/waits/poll": "woke",
		"/api/pause":              "before after",
		"/api/events":             "data: 1\n\ndata: 2\n\n",
	} {
		req, _ := http.NewRequest("GET", "http://x"+path, nil)
		resp, err := rig.sess.RoundTrip(req)
		require.NoError(t, err, path)
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		assert.NoError(t, err, "%s ends complete, not truncated", path)
		assert.Equal(t, want, string(b), path)
	}
}

func TestEventStreamContentType(t *testing.T) {
	assert.True(t, eventStream("text/event-stream"))
	assert.True(t, eventStream("Text/Event-Stream; charset=utf-8"))
	assert.False(t, eventStream("application/json"))
	assert.False(t, eventStream(""))
}
