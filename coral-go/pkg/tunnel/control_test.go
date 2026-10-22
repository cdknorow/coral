package tunnel

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5918: the connector tells the hub when this computer's maximum mode changes: POST /control/mode {"local_mode":...}, a connector-opened stream of its own kind, advertised as
// local_mode/1, 204 on success, at most one a second.

func modeHub(t *testing.T, caps []string, on LocalModeFunc, gap time.Duration) *hubHarness {
	t.Helper()
	cfg := SessionConfig{OnLocalMode: on, MinLocalModeGap: gap}
	if caps != nil {
		cfg.Capabilities = func(any) []string { return caps }
	}
	return newHub(t, cfg)
}

func TestTheLocalModeMessageReachesTheHubAndIsRecorded(t *testing.T) {
	var mu sync.Mutex
	var got []Mode
	h := modeHub(t, []string{CapabilityLocalMode}, func(_ context.Context, s *Session, m Mode) {
		assert.Equal(t, "server-1", s.Identity(), "the machine is the tunnel, never the body")
		assert.Equal(t, m, s.LocalMode(), "the session's mode is already the new one")
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	}, 20*time.Millisecond)
	hubSide, cs := connect(t, h, noopLocal(), nil)
	require.True(t, cs.CanSendLocalMode())
	assert.True(t, hubSide.HasPeerCapability(CapabilityLocalMode), "the connector says it in its Hello")
	require.Equal(t, TierView, hubSide.LocalMode())
	for _, m := range []Mode{TierLaunch, TierInteract, TierView} {
		require.NoError(t, cs.SendLocalMode(context.Background(), m), m.String())
		assert.Equal(t, m, hubSide.LocalMode())
		time.Sleep(40 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []Mode{TierLaunch, TierInteract, TierView}, got)
	assert.Error(t, cs.SendLocalMode(context.Background(), TierNever), "never is not a mode")
	assert.Error(t, cs.SendLocalMode(context.Background(), Mode(9)))
}

// Advertised only when the hub decides so AND serves it.
func TestTheLocalModeMessageIsNotSentToAHubThatDidNotAdvertiseIt(t *testing.T) {
	for name, h := range map[string]*hubHarness{
		"no capabilities hook": modeHub(t, nil, func(context.Context, *Session, Mode) {}, 0),
		"hook says relay only": modeHub(t, []string{CapabilityRelay}, func(context.Context, *Session, Mode) {}, 0),
		"no OnLocalMode":       modeHub(t, []string{CapabilityLocalMode}, nil, 0),
	} {
		_, cs := connect(t, h, noopLocal(), nil)
		assert.False(t, cs.CanSendLocalMode(), name)
		assert.ErrorIs(t, cs.SendLocalMode(context.Background(), TierInteract), ErrLocalModeUnsupported, name)
	}
}

// The body is exactly {"local_mode":"view|interact|launch"} and nothing else.
func TestTheLocalModeBodyIsExactlyTheMessage(t *testing.T) {
	for body, want := range map[string]Mode{
		`{"local_mode":"view"}`:     TierView,
		`{"local_mode":"interact"}`: TierInteract,
		`{"local_mode":"launch"}`:   TierLaunch,
		` {"local_mode":"view"} `:   TierView, // whitespace around the JSON is not another field
	} {
		m, ok := parseLocalModeBody([]byte(body))
		assert.True(t, ok, body)
		assert.Equal(t, want, m, body)
	}
	for _, body := range []string{
		``, `{}`, `null`, `[]`, `"view"`, `{"local_mode":""}`, `{"local_mode":"View"}`, `{"local_mode":" view"}`, `{"local_mode":"never"}`, `{"local_mode":"admin"}`, `{"local_mode":1}`,
		`{"local_mode":"view","x":1}`, `{"mode":"view"}`, `{"local_mode":"view"}{"local_mode":"launch"}`, `{"local_mode":"view"} x`, `{"local_mode":"view","local_mode":"launch"}`,
	} {
		_, ok := parseLocalModeBody([]byte(body))
		assert.False(t, ok, "refused: %q", body)
	}
	// on the wire: a body that is not exactly the message is a 400 and changes nothing
	var calls int
	h := modeHub(t, []string{CapabilityLocalMode}, func(context.Context, *Session, Mode) { calls++ }, 0)
	hubSide, cs := connect(t, h, noopLocal(), nil)
	for _, body := range []string{`{"local_mode":"view","extra":true}`, `{"local_mode":"root"}`, `nope`} {
		r, err := cs.callInbound(context.Background(), controlKind, ControlModePath, []byte(body), ErrLocalModeUnsupported)
		require.NoError(t, err, body)
		assert.Equal(t, 400, r.Status, body)
	}
	assert.Zero(t, calls)
	assert.Equal(t, TierView, hubSide.LocalMode())
	good, _ := json.Marshal(localModeBody{LocalMode: "interact"})
	r, err := cs.callInbound(context.Background(), controlKind, ControlModePath, good, ErrLocalModeUnsupported)
	require.NoError(t, err)
	assert.Equal(t, 204, r.Status)
	assert.Equal(t, TierInteract, hubSide.LocalMode())
	// no other path is a control request
	_, err = cs.callInbound(context.Background(), controlKind, "/control/other", good, ErrLocalModeUnsupported)
	assert.Error(t, err)
}

// At most about one a second from one tunnel: a faster message is 429 with Retry-After and is not recorded.
func TestAFasterLocalModeMessageIsRefusedWith429(t *testing.T) {
	var calls int
	h := modeHub(t, []string{CapabilityLocalMode}, func(context.Context, *Session, Mode) { calls++ }, 300*time.Millisecond)
	hubSide, cs := connect(t, h, noopLocal(), nil)
	require.NoError(t, cs.SendLocalMode(context.Background(), TierInteract))
	err := cs.SendLocalMode(context.Background(), TierLaunch)
	require.ErrorIs(t, err, ErrLocalModeRefused)
	assert.Contains(t, err.Error(), "429")
	assert.Equal(t, 1, calls)
	assert.Equal(t, TierInteract, hubSide.LocalMode(), "the refused value was not taken")
	time.Sleep(350 * time.Millisecond)
	require.NoError(t, cs.SendLocalMode(context.Background(), TierLaunch), "after the gap")
	assert.Equal(t, TierLaunch, hubSide.LocalMode())
}

// A hub that does not answer: the connector gives up at its context (the manager's fall back is a reconnect).
func TestALocalModeMessageTheHubNeverAnswersEndsWithTheContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h := modeHub(t, []string{CapabilityLocalMode}, func(context.Context, *Session, Mode) { <-release }, 0)
	_, cs := connect(t, h, noopLocal(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := cs.SendLocalMode(ctx, TierInteract)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrLocalModeUnsupported, "a timeout is not 'the hub does not take it'")
}

// Lowering this computer's mode ends the open streams whose viewer was allowed more than the new mode, and only those.
func TestCloseAboveEndsTheStreamsTheViewerWasAllowedMoreThanTheNewMode(t *testing.T) {
	l := &LocalHandler{}
	ended := map[string]int{}
	var mu sync.Mutex
	reg := func(name string, viewer Tier) func() {
		return l.registerWS(viewer, func() { mu.Lock(); ended[name]++; mu.Unlock() })
	}
	view, interact, launch := reg("view", TierView), reg("interact", TierInteract), reg("launch", TierLaunch)
	assert.Equal(t, 0, l.CloseAbove(TierLaunch), "a raise, or no change: nothing is above it")
	assert.Equal(t, 1, l.CloseAbove(TierInteract), "lowered to Interact: only the Launch viewer")
	assert.Equal(t, map[string]int{"launch": 1}, ended)
	assert.Equal(t, 2, l.CloseAbove(TierView), "lowered to View: the Interact and the Launch viewers, never the View one")
	assert.Equal(t, 1, ended["interact"])
	assert.Equal(t, 0, ended["view"], "a viewer who could only look keeps looking")
	launch()
	interact()
	assert.Equal(t, 0, l.CloseAbove(TierView), "a stream that ended is no longer tracked")
	view()
	assert.Empty(t, l.openWSs)
}
