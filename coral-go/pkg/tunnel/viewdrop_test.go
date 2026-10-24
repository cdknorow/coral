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

const waitingRow = `[{"session_id":"s1","waiting_summary":"Claude needs your permission to use Bash: rm -rf <dir>","waiting_for_input":true,"held_messages":[{"id":"h1","text":"queued secret words","held_at":"2026-10-23T12:00:00Z","sending":false}],"big":12345678901234567890,"note":"a < b && c > d"}]`

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
		assert.Nil(t, rows[0]["held_messages"], path+": a person's queued messages are not for a viewer who cannot send")
		assert.NotContains(t, body, "queued secret words", path)
		assert.Equal(t, true, rows[0]["waiting_for_input"], path+": the state stays, only the text goes")
		assert.Contains(t, body, `12345678901234567890`, path+": numbers go back as they came")
		assert.Contains(t, body, `a < b && c > d`, path+": no HTML escaping")
		assert.Empty(t, resp.Header.Get(BodyLengthHeader), path+": the upstream length is not announced for a changed body")

		_, noTier := get(path, ctx)
		assert.NotContains(t, noTier, "rm -rf", path+": a stream with no tier (an older hub) is View")

		for _, tier := range []string{"interact", "launch"} {
			_, body = get(path, WithHubTier(ctx, tier))
			assert.Contains(t, body, "rm -rf", path+": "+tier+" gets the line")
			assert.Contains(t, body, "queued secret words", path+": "+tier+" can send, so it sees what is queued")
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
	assert.NotContains(t, f[0].data, "queued secret words", "the queued chat messages too")
	assert.Contains(t, f[0].data, `"waiting_for_input":true`)
	assert.True(t, json.Valid([]byte(f[0].data)))

	view = false // the viewer's tier (or the computer's mode) is read for every frame
	require.NoError(t, w.write(websocket.MessageText, []byte(frame)))
	f = dst.got()
	require.Len(t, f, 2)
	assert.Contains(t, f[1].data, "rm -rf", "an Interact viewer gets the line")
	assert.Contains(t, f[1].data, "queued secret words")

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
	assert.NotContains(t, first("view"), "queued secret words", "nor the queued chat messages")
	assert.Contains(t, first("interact"), "rm -rf", "an Interact viewer's feed has the line")
	assert.Contains(t, first("launch"), "rm -rf")
	mode = TierView
	assert.NotContains(t, first("launch"), "rm -rf", "the computer's mode lowered to View caps the viewer")
}

// #7116: the boards' goal counts are nulled for a viewer who can only look, wherever the feed or the projects list carries them; the goals' own list (a goals array) keeps its goals.
func TestGoalsSummaryIsNulledForAViewOnlyViewer(t *testing.T) {
	assert.True(t, dropsFor(http.MethodGet, "/api/board/projects"), "the projects list carries each board's brief")
	assert.True(t, dropsFor(http.MethodGet, "/api/board/team/goals"), "the goals' own list drops the blocked goal's fields (#7087), and only those: its goals array stays (below)")

	for name, in := range map[string]string{
		"the projects list": `[{"project":"team","goals_brief":{"open":2,"seq":9},"subscriber_count":3}]`,
		"a full frame":      `{"type":"coral_update","sessions":[],"goals_brief":{"team":{"open":2,"seq":9}},"goals_open":2,"goals_seq":9}`,
		"a diff frame":      `{"type":"coral_diff","goals_brief":{"team":{"open":1,"seq":10}},"goals_open":1,"goals_seq":10}`,
	} {
		out, ok := stripViewDropped([]byte(in))
		require.True(t, ok, name)
		var v any
		require.NoError(t, json.Unmarshal(out, &v), name)
		raw := string(out)
		assert.NotContains(t, raw, `"open"`, name+": no count survives")
		assert.NotContains(t, raw, `"seq"`, name)
		assert.True(t, strings.Contains(raw, `"goals_brief":null`), name+": nulled, not removed: "+raw)
		if strings.Contains(in, "goals_open") {
			assert.Contains(t, raw, `"goals_open":null`, name)
			assert.Contains(t, raw, `"goals_seq":null`, name)
		}
	}
	// The goals list's own answer has a goals key and keeps its goals: the brief is goals_brief, not goals.
	list, ok := stripViewDropped([]byte(`{"goals":[{"id":1,"title":"Ship","rollup":{"open":1}}]}`))
	require.True(t, ok)
	assert.JSONEq(t, `{"goals":[{"id":1,"title":"Ship","rollup":{"open":1}}]}`, string(list))
	// What is not a goal count is as it was.
	out, ok := stripViewDropped([]byte(`[{"project":"team","subscriber_count":3}]`))
	require.True(t, ok)
	assert.JSONEq(t, `[{"project":"team","subscriber_count":3}]`, string(out))
}

// #7116 (coral-hub's condition for pinning 167): on a View viewer's /ws/coral stream the goals brief and its totals are null in the first coral_update AND in every coral_diff that carries them, an Interact
// viewer reads the numbers in both, and the tier is read for every frame (a viewer lowered mid-stream stops getting them at the next frame).
const (
	goalsUpdateFrame = `{"type":"coral_update","sessions":[],"goals_brief":{"team-a":{"open":2,"seq":14}},"goals_open":2,"goals_seq":14}`
	goalsDiffFrame   = `{"type":"coral_diff","goals_brief":{"team-a":{"open":1,"seq":15},"team-b":{"open":3,"seq":4}},"goals_open":4,"goals_seq":15}`
)

func TestGoalsBriefIsNullInEveryFeedFrameForAViewViewer(t *testing.T) {
	view := true
	dst := &wsRecorder{}
	w := newWSRedact(context.Background(), dst, secrets.Redactor(), func() bool { return false }, false)
	w.viewOnly = func() bool { return view }
	nulled := func(data string) {
		t.Helper()
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &m), data)
		for _, k := range []string{"goals_brief", "goals_open", "goals_seq"} {
			v, present := m[k]
			assert.True(t, present, k+" is nulled, not removed: "+data)
			assert.Nil(t, v, k+": "+data)
		}
		assert.NotContains(t, data, `"open"`)
		assert.NotContains(t, data, `"seq"`)
	}

	require.NoError(t, w.write(websocket.MessageText, []byte(goalsUpdateFrame)))
	require.NoError(t, w.write(websocket.MessageText, []byte(goalsDiffFrame)))
	require.NoError(t, w.write(websocket.MessageText, []byte(goalsDiffFrame)))
	f := dst.got()
	require.Len(t, f, 3)
	for _, fr := range f {
		nulled(fr.data)
	}
	assert.Contains(t, f[0].data, `"type":"coral_update"`)
	assert.Contains(t, f[1].data, `"type":"coral_diff"`)

	view = false // an Interact viewer reads the numbers, in the update and in the diff
	require.NoError(t, w.write(websocket.MessageText, []byte(goalsUpdateFrame)))
	require.NoError(t, w.write(websocket.MessageText, []byte(goalsDiffFrame)))
	f = dst.got()
	require.Len(t, f, 5)
	assert.JSONEq(t, goalsUpdateFrame, f[3].data)
	assert.JSONEq(t, goalsDiffFrame, f[4].data)

	view = true // lowered mid-stream: the very next diff is nulled again
	require.NoError(t, w.write(websocket.MessageText, []byte(goalsDiffFrame)))
	f = dst.got()
	require.Len(t, f, 6)
	nulled(f[5].data)
}

// The whole path through the connector: the local feed sends an update and then a diff; a View viewer's socket gets both with the brief nulled, an Interact viewer's with the numbers.
func TestGoalsBriefThroughTheConnectorIsNullForAViewerInUpdateAndDiff(t *testing.T) {
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = c.Write(r.Context(), websocket.MessageText, []byte(goalsUpdateFrame))
		_ = c.Write(r.Context(), websocket.MessageText, []byte(goalsDiffFrame))
		c.Read(r.Context())
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.MaxMode = func() Mode { return TierLaunch }
		l.AllowPath = nil
		l.AllowRequest = AllowRequest(func() Mode { return TierLaunch })
		l.RedactTokens = func() bool { return false }
	})
	frames := func(tier string) []string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c, err := sess.DialWS(WithHubTier(WithHubUser(ctx, "hub-user-1234abcd"), tier), "/ws/coral", "", nil)
		require.NoError(t, err)
		defer c.CloseNow()
		var out []string
		for i := 0; i < 2; i++ {
			_, data, err := c.Read(ctx)
			require.NoError(t, err)
			out = append(out, string(data))
		}
		return out
	}
	for _, fr := range frames("view") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(fr), &m), fr)
		for _, k := range []string{"goals_brief", "goals_open", "goals_seq"} {
			v, present := m[k]
			assert.True(t, present, k+": "+fr)
			assert.Nil(t, v, k+" is null for a View viewer: "+fr)
		}
	}
	got := frames("interact")
	assert.JSONEq(t, goalsUpdateFrame, got[0])
	assert.JSONEq(t, goalsDiffFrame, got[1])
}
