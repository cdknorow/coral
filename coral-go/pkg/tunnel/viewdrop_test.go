package tunnel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secrets"
)

// #6592 (Security's #6581 rule): a viewer who can only look never receives a live row's waiting_summary, whatever the owner's redaction switch says; Interact and Launch do.

const waitingRow = `[{"session_id":"s1","waiting_summary":"Claude needs your permission to use Bash: rm -rf <dir>","waiting_for_input":true,"big":12345678901234567890,"note":"a < b && c > d"}]`

func TestViewOnlyViewerGetsNoWaitingSummaryOverHTTP(t *testing.T) {
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions/live", "/api/sessions/s1/resolve", "/api/sessions/s1/status", "/api/sessions/live/a/chat":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", strconv.Itoa(len(waitingRow)))
			io.WriteString(w, waitingRow)
		case "/api/sessions/live/a/capture":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, `"waiting_summary":"plain text is not a document"`)
		}
	})
	hub := newHub(t, fastCfg())
	mode := TierLaunch
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.MaxMode = func() Mode { return mode }
		l.AllowPath = nil
		l.AllowRequest = AllowRequest(func() Mode { return mode })
		l.RedactTokens = func() bool { return false } // the owner's secrets switch is off: dropping the field does not depend on it
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	get := func(path string, ctx context.Context) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://x"+path, nil)
		resp, err := sess.RoundTrip(req)
		require.NoError(t, err, path)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err, path)
		return resp, string(b)
	}
	routes := []string{"/api/sessions/live", "/api/sessions/s1/resolve", "/api/sessions/s1/status"}

	for _, path := range routes {
		resp, body := get(path, WithHubTier(ctx, "view"))
		require.Equal(t, 200, resp.StatusCode, path)
		var rows []map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &rows), body)
		assert.Nil(t, rows[0]["waiting_summary"], path+": View gets no text")
		assert.Contains(t, body, `"waiting_summary":null`, path)
		assert.NotContains(t, body, "rm -rf", path)
		assert.Equal(t, true, rows[0]["waiting_for_input"], path+": the state stays, only the text goes")
		assert.Contains(t, body, `12345678901234567890`, path+": numbers go back as they came")
		assert.Contains(t, body, `a < b && c > d`, path+": no HTML escaping")
		assert.Empty(t, resp.Header.Get(BodyLengthHeader), path+": the upstream length is not announced for a changed body")

		_, noTier := get(path, ctx)
		assert.NotContains(t, noTier, "rm -rf", path+": a stream with no tier (an older hub) is View")

		for _, tier := range []string{"interact", "launch"} {
			_, body = get(path, WithHubTier(ctx, tier))
			assert.Contains(t, body, "rm -rf", path+": "+tier+" gets the line")
		}
	}

	// A route that is not in the list, and a reply that is not JSON, pass as they are.
	_, body := get("/api/sessions/live/a/chat", WithHubTier(ctx, "view"))
	assert.Contains(t, body, "rm -rf", "only the routes that carry a live row are changed")
	_, body = get("/api/sessions/live/a/capture", WithHubTier(ctx, "view"))
	assert.Contains(t, body, "plain text is not a document")

	// The computer's mode lowered to View caps a viewer of Launch.
	mode = TierView
	for _, path := range routes {
		_, body = get(path, WithHubTier(ctx, "launch"))
		assert.NotContains(t, body, "rm -rf", path+": the lower of the viewer's tier and the computer's mode")
	}
}

func TestViewOnlyViewerGetsNoWaitingSummaryOnTheLiveFeed(t *testing.T) {
	view := true
	on := false
	dst := &wsRecorder{}
	w := newWSRedact(context.Background(), dst, secrets.Redactor(), func() bool { return on }, false)
	w.viewOnly = func() bool { return view }
	frame := `{"type":"coral_update","sessions":` + waitingRow + `,"teams":[{"agents":[{"waiting_summary":"nested text"}]}]}`

	require.NoError(t, w.write(websocket.MessageText, []byte(frame)))
	f := dst.got()
	require.Len(t, f, 1)
	assert.NotContains(t, f[0].data, "rm -rf", "dropped with the secrets switch off")
	assert.NotContains(t, f[0].data, "nested text", "wherever the key is")
	assert.Contains(t, f[0].data, `"waiting_for_input":true`)
	assert.True(t, json.Valid([]byte(f[0].data)))

	view = false // the viewer's tier (or the computer's mode) is read for every frame
	require.NoError(t, w.write(websocket.MessageText, []byte(frame)))
	f = dst.got()
	require.Len(t, f, 2)
	assert.Contains(t, f[1].data, "rm -rf", "an Interact viewer gets the line")

	view = true
	require.Error(t, w.write(websocket.MessageText, []byte(`{"waiting_summary":"a command", broken`)), "a frame that has the key and cannot be read is never sent on")
	assert.NotEqual(t, websocket.StatusCode(0), dst.closed, "and the stream is closed")
}

func TestStripViewDropped(t *testing.T) {
	same := []byte(`{"a":1,"note":"waiting_summary is a word here"}`)
	out, ok := stripViewDropped(same)
	assert.True(t, ok)
	assert.Equal(t, same, out, "a document without the key as a key is returned as it is")

	out, ok = stripViewDropped([]byte(`{"waiting_summary":"x","n":[{"waiting_summary":{"deep":"y"}}]}`))
	assert.True(t, ok)
	assert.JSONEq(t, `{"waiting_summary":null,"n":[{"waiting_summary":null}]}`, string(out))

	deep := strings.Repeat(`[`, 100) + `{"waiting_summary":"x"}` + strings.Repeat(`]`, 100)
	_, ok = stripViewDropped([]byte(deep))
	assert.False(t, ok, "a document nested past the bound is refused, not sent on")
}

// The whole path: a viewer's socket to /ws/coral through the connector. A View viewer's feed frames carry no text; an Interact viewer's do; the computer's mode lowered to View caps it.
func TestLiveFeedThroughTheConnectorDropsWaitingSummaryForAViewer(t *testing.T) {
	frame := `{"type":"coral_update","sessions":` + waitingRow + `}`
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = c.Write(r.Context(), websocket.MessageText, []byte(frame))
		c.Read(r.Context()) // held open until the test closes it
	})
	hub := newHub(t, fastCfg())
	mode := TierLaunch
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.MaxMode = func() Mode { return mode }
		l.AllowPath = nil
		l.AllowRequest = AllowRequest(func() Mode { return mode })
		l.RedactTokens = func() bool { return false }
	})
	first := func(tier string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c, err := sess.DialWS(WithHubTier(WithHubUser(ctx, "hub-user-1234abcd"), tier), "/ws/coral", "", nil)
		require.NoError(t, err)
		defer c.CloseNow()
		_, data, err := c.Read(ctx)
		require.NoError(t, err)
		return string(data)
	}
	assert.NotContains(t, first("view"), "rm -rf", "a View viewer's feed has no text")
	assert.Contains(t, first("interact"), "rm -rf", "an Interact viewer's feed has the line")
	assert.Contains(t, first("launch"), "rm -rf")
	mode = TierView
	assert.NotContains(t, first("launch"), "rm -rf", "the computer's mode lowered to View caps the viewer")
}
