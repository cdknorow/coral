package hubproxy

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWS is a scripted WSConn: Read returns the queued frames, then an error; Write
// records what was written.
type fakeWS struct {
	mu      sync.Mutex
	reads   [][]byte
	written [][]byte
}

func (f *fakeWS) Read(context.Context) (websocket.MessageType, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reads) == 0 {
		return 0, nil, errors.New("done")
	}
	d := f.reads[0]
	f.reads = f.reads[1:]
	return websocket.MessageText, d, nil
}
func (f *fakeWS) Write(_ context.Context, _ websocket.MessageType, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, append([]byte(nil), p...))
	return nil
}
func (f *fakeWS) Close(websocket.StatusCode, string) error { return nil }
func (f *fakeWS) CloseNow() error                          { return nil }
func (f *fakeWS) Subprotocol() string                      { return "" }
func (f *fakeWS) SetReadLimit(int64)                       {}

func notices(t *testing.T, f *fakeWS) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, w := range f.written {
		var m map[string]any
		require.NoError(t, json.Unmarshal(w, &m))
		out = append(out, m)
	}
	return out
}

// #3276: a read-only hub answers each dropped input that carries an id with its
// hub_read_only notice naming that id, so the dashboard knows which send was refused.
func TestReadOnlyNoticeEchoesTheDroppedFramesID(t *testing.T) {
	browser := &fakeWS{reads: [][]byte{
		[]byte(`{"type":"terminal_input","data":"ls","id":7}`),
		[]byte(`{"type":"terminal_input","data":"\r","id":8}`),
	}}
	remote := &fakeWS{}
	gate := clientGate{
		allow: func() bool { return false },
		notice: func() map[string]any {
			return map[string]any{"type": "hub_read_only", "required": "interact", "hub_mode": "view"}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	pumpFrom(ctx, cancel, browser, remote, "lost", gate, nil)

	assert.Empty(t, remote.written, "nothing reached the terminal")
	got := notices(t, browser)
	require.Len(t, got, 2, "one notice per dropped input with an id")
	for i, id := range []float64{7, 8} {
		assert.Equal(t, "hub_read_only", got[i]["type"])
		assert.Equal(t, "interact", got[i]["required"])
		assert.Equal(t, "view", got[i]["hub_mode"])
		assert.Equal(t, id, got[i]["id"])
	}
}

// Keystrokes without an id get the plain notice, at most once per readOnlyNoticeEvery.
func TestReadOnlyNoticeWithoutAnIDIsRateLimited(t *testing.T) {
	browser := &fakeWS{reads: [][]byte{
		[]byte(`{"type":"terminal_input","data":"a"}`),
		[]byte(`{"type":"terminal_input","data":"b"}`),
		[]byte(`{"type":"terminal_input","data":"c"}`),
	}}
	gate := clientGate{allow: func() bool { return false }, notice: func() map[string]any { return map[string]any{"type": "hub_read_only"} }}
	ctx, cancel := context.WithCancel(context.Background())
	pumpFrom(ctx, cancel, browser, &fakeWS{}, "lost", gate, nil)
	got := notices(t, browser)
	require.Len(t, got, 1)
	assert.NotContains(t, got[0], "id")
}

// No notice configured (an embedding hub that hasn't opted in): dropped silently, as before.
func TestReadOnlyWithoutANoticeDropsSilently(t *testing.T) {
	browser := &fakeWS{reads: [][]byte{[]byte(`{"type":"terminal_input","data":"ls","id":1}`)}}
	remote := &fakeWS{}
	ctx, cancel := context.WithCancel(context.Background())
	pumpFrom(ctx, cancel, browser, remote, "lost", clientGate{allow: func() bool { return false }}, nil)
	assert.Empty(t, browser.written)
	assert.Empty(t, remote.written)
}

// Only an integer id is taken from a JSON object frame; anything else is no id.
func TestFrameID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		id   int64
		want bool
	}{
		{`{"type":"terminal_input","id":42}`, 42, true},
		{`{"id":0}`, 0, true},
		{`{"id":-1}`, 0, false},
		{`{"id":1.5}`, 0, false},
		{`{"id":"42"}`, 0, false},
		{`{"id":"<script>"}`, 0, false},
		{`{"id":99999999999999999999}`, 0, false},
		{`{"id":9007199254740993}`, 0, false},
		{`{"type":"terminal_input"}`, 0, false},
		{`ls`, 0, false},
		{`[1,2]`, 0, false},
		{`{"id":`, 0, false},
	} {
		id, ok := frameID(websocket.MessageText, []byte(tc.in))
		assert.Equal(t, tc.want, ok, tc.in)
		if tc.want {
			assert.Equal(t, tc.id, id, tc.in)
		}
	}
	_, ok := frameID(websocket.MessageBinary, []byte(`{"id":1}`))
	assert.False(t, ok, "binary frames have no id")
}
