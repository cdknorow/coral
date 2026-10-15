package tunnel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

// #3489: the relay, the one stream kind the connector opens.

func relayHub(t *testing.T, caps []string, on RelayFunc) *hubHarness {
	t.Helper()
	cfg := SessionConfig{OnRelay: on}
	if caps != nil {
		cfg.Capabilities = func(identity any) []string {
			require.Equal(t, "server-1", identity, "the hook sees the authenticated tunnel")
			return caps
		}
	}
	return newHub(t, cfg)
}

func noopLocal() http.Handler { return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}) }

// A relay request reaches OnRelay with its path and body, on the session it came from,
// and the hub's status, body and Retry-After come back.
func TestRelayRoundTrip(t *testing.T) {
	var got []RelayRequest
	var mu sync.Mutex
	h := relayHub(t, []string{CapabilityRelay}, func(ctx context.Context, s *Session, req RelayRequest) RelayResponse {
		mu.Lock()
		got = append(got, req)
		mu.Unlock()
		assert.Equal(t, "server-1", s.Identity(), "the source is the tunnel, never the request")
		if req.Path == RelayTargetsPath {
			return RelayResponse{Status: http.StatusOK, Body: []byte(`{"ok":true,"targets":[]}`)}
		}
		return RelayResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"ok":false,"error":"rate_limited"}`), RetryAfter: 7}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	require.True(t, cs.CanRelay())
	assert.Equal(t, 2, cs.Version())

	resp, err := cs.Relay(context.Background(), RelayTargetsPath, []byte(`{"from_board":"qa"}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.Status)
	assert.JSONEq(t, `{"ok":true,"targets":[]}`, string(resp.Body))

	resp, err = cs.Relay(context.Background(), RelayMessagePath, []byte(`{"message_id":"m1"}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, resp.Status)
	assert.Equal(t, 7, resp.RetryAfter)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 2)
	assert.Equal(t, RelayTargetsPath, got[0].Path)
	assert.Equal(t, `{"from_board":"qa"}`, string(got[0].Body))
	assert.Equal(t, RelayMessagePath, got[1].Path)
}

// The relay is advertised only when the hub decides so for the tunnel AND has a handler;
// otherwise the connector never opens a relay stream (ErrRelayUnsupported locally).
func TestRelayNotAdvertisedWithoutTheHubsYes(t *testing.T) {
	for name, h := range map[string]*hubHarness{
		"no capabilities hook":    relayHub(t, nil, func(context.Context, *Session, RelayRequest) RelayResponse { return RelayResponse{Status: 200} }),
		"hook says no":            relayHub(t, []string{}, func(context.Context, *Session, RelayRequest) RelayResponse { return RelayResponse{Status: 200} }),
		"no OnRelay handler":      relayHub(t, []string{CapabilityRelay}, nil),
		"unknown capability only": relayHub(t, []string{"teleport"}, func(context.Context, *Session, RelayRequest) RelayResponse { return RelayResponse{Status: 200} }),
	} {
		_, cs := connect(t, h, noopLocal(), nil)
		assert.False(t, cs.CanRelay(), name)
		_, err := cs.Relay(context.Background(), RelayMessagePath, []byte(`{}`))
		assert.ErrorIs(t, err, ErrRelayUnsupported, name)
	}
}

// The client refuses what isn't a relay request before opening a stream.
func TestRelayClientChecks(t *testing.T) {
	h := relayHub(t, []string{CapabilityRelay}, func(context.Context, *Session, RelayRequest) RelayResponse { return RelayResponse{Status: 200} })
	_, cs := connect(t, h, noopLocal(), nil)
	_, err := cs.Relay(context.Background(), "/api/board/x/messages", []byte(`{}`))
	assert.Error(t, err)
	_, err = cs.Relay(context.Background(), RelayTargetsPath, make([]byte, RelayMaxTargets+1))
	assert.Error(t, err)
	_, err = cs.Relay(context.Background(), RelayMessagePath, make([]byte, RelayMaxMessage+1))
	assert.Error(t, err)
}

// On a relay-enabled tunnel, every connector-opened stream that is not exactly a relay
// request is closed unanswered, and OnRelay never runs for it: another kind, another
// path, a query, headers, a body over the path's cap, or a body shorter than announced.
func TestHubClosesEveryOtherInboundStream(t *testing.T) {
	var calls atomic.Int32
	h := relayHub(t, []string{CapabilityRelay}, func(context.Context, *Session, RelayRequest) RelayResponse {
		calls.Add(1)
		return RelayResponse{Status: 200}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	send := func(hdr Header, body []byte) error {
		st, err := cs.ys.OpenStream()
		require.NoError(t, err)
		defer st.Close()
		_ = st.SetDeadline(time.Now().Add(10 * time.Second))
		require.NoError(t, writeFrame(st, hdr))
		if body != nil {
			_ = writeRawFrame(st, body)
		}
		_, err = readFrame(st, 1<<16)
		return err
	}
	small := []byte(`{}`)
	for name, tc := range map[string]struct {
		hdr  Header
		body []byte
	}{
		"http kind":       {Header{Kind: KindHTTP, Method: "POST", Path: "/api/board/x/external-messages", ContentLength: 2}, small},
		"ws kind":         {Header{Kind: KindWS, Path: "/ws/coral"}, nil},
		"other path":      {Header{Kind: KindRelay, Method: "POST", Path: "/relay/v1/links", ContentLength: 2}, small},
		"GET":             {Header{Kind: KindRelay, Method: "GET", Path: RelayMessagePath, ContentLength: 2}, small},
		"query":           {Header{Kind: KindRelay, Method: "POST", Path: RelayMessagePath, Query: "x=1", ContentLength: 2}, small},
		"headers":         {Header{Kind: KindRelay, Method: "POST", Path: RelayMessagePath, Headers: http.Header{"X-Coral-Hub-Server": {"x"}}, ContentLength: 2}, small},
		"over the cap":    {Header{Kind: KindRelay, Method: "POST", Path: RelayTargetsPath, ContentLength: RelayMaxTargets + 1}, make([]byte, RelayMaxTargets+1)},
		"short body":      {Header{Kind: KindRelay, Method: "POST", Path: RelayMessagePath, ContentLength: 10}, small},
		"negative length": {Header{Kind: KindRelay, Method: "POST", Path: RelayMessagePath, ContentLength: -1}, small},
	} {
		err := send(tc.hdr, tc.body)
		assert.Error(t, err, "%s: closed without an answer", name)
	}
	assert.Equal(t, int32(0), calls.Load(), "OnRelay never ran")
}

// A relay header that never arrives is dropped at the read deadline, before any hook.
func TestHubDropsASilentRelayStream(t *testing.T) {
	var calls atomic.Int32
	h := relayHub(t, []string{CapabilityRelay}, func(context.Context, *Session, RelayRequest) RelayResponse {
		calls.Add(1)
		return RelayResponse{Status: 200}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	st, err := cs.ys.OpenStream()
	require.NoError(t, err)
	defer st.Close()
	_, _ = st.Write([]byte{0}) // half a length prefix, then nothing
	_ = st.SetReadDeadline(time.Now().Add(relayReadDeadline + 5*time.Second))
	_, err = io.ReadAll(st)
	assert.NoError(t, err, "the hub closed it (EOF)")
	assert.Equal(t, int32(0), calls.Load())
}

// At most MaxRelayInFlight relay requests per tunnel run at once; more get 429.
func TestRelayInFlightCap(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	h := newHub(t, SessionConfig{
		Capabilities:     func(any) []string { return []string{CapabilityRelay} },
		MaxRelayInFlight: 1,
		OnRelay: func(ctx context.Context, s *Session, req RelayRequest) RelayResponse {
			started <- struct{}{}
			<-release
			return RelayResponse{Status: 200, Body: []byte(`{"ok":true}`)}
		},
	})
	_, cs := connect(t, h, noopLocal(), nil)
	first := make(chan RelayResponse, 1)
	go func() {
		r, _ := cs.Relay(context.Background(), RelayMessagePath, []byte(`{}`))
		first <- r
	}()
	<-started
	r, err := cs.Relay(context.Background(), RelayMessagePath, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, r.Status)
	close(release)
	assert.Equal(t, http.StatusOK, (<-first).Status)
}

// A version 1 connector (older Coral) is accepted as before and never gets a capability.
func TestVersion1PeerGetsNoCapabilities(t *testing.T) {
	h := relayHub(t, []string{CapabilityRelay}, func(context.Context, *Session, RelayRequest) RelayResponse { return RelayResponse{Status: 200} })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, h.URL(), nil)
	require.NoError(t, err)
	defer c.Close(websocket.StatusNormalClosure, "")
	hello, _ := json.Marshal(Hello{ProtocolVersion: 1, ServerToken: testToken, CoralVersion: "old"})
	require.NoError(t, c.Write(ctx, websocket.MessageBinary, hello))
	_, msg, err := c.Read(ctx)
	require.NoError(t, err)
	var w Welcome
	require.NoError(t, json.Unmarshal(msg, &w))
	assert.Equal(t, 1, w.AcceptedVersion)
	assert.Empty(t, w.Capabilities)
	s := h.next(t)
	assert.False(t, s.relayOn)
}

// The per-tunnel stream limit is taken before any goroutine or read: with MaxRelayStreams
// silent streams open, one more is closed at once (not after the read deadline), and the
// tunnel still serves relays once those go.
func TestRelayStreamFloodStaysBounded(t *testing.T) {
	h := newHub(t, SessionConfig{
		Capabilities:    func(any) []string { return []string{CapabilityRelay} },
		MaxRelayStreams: 2,
		OnRelay: func(context.Context, *Session, RelayRequest) RelayResponse {
			return RelayResponse{Status: 200, Body: []byte(`{"ok":true}`)}
		},
	})
	_, cs := connect(t, h, noopLocal(), nil)
	var silent []io.Closer
	for i := 0; i < 2; i++ {
		st, err := cs.ys.OpenStream()
		require.NoError(t, err)
		_, _ = st.Write([]byte{0}) // half a length prefix: holds a slot until the deadline
		silent = append(silent, st)
	}
	time.Sleep(200 * time.Millisecond)
	extra, err := cs.ys.OpenStream()
	require.NoError(t, err)
	_, _ = extra.Write([]byte{0})
	start := time.Now()
	_ = extra.SetReadDeadline(time.Now().Add(relayReadDeadline + 5*time.Second))
	_, _ = io.ReadAll(extra)
	assert.Less(t, time.Since(start), relayReadDeadline/2, "the stream over the limit is closed at once")
	for _, c := range silent {
		_ = c.Close()
	}
	require.Eventually(t, func() bool {
		r, err := cs.Relay(context.Background(), RelayMessagePath, []byte(`{}`))
		return err == nil && r.Status == http.StatusOK
	}, 2*relayReadDeadline, 200*time.Millisecond, "relays work again once the slots free up")
}

// A panicking OnRelay answers 502 target_error, and the hub keeps serving the tunnel.
func TestRelayHandlerPanicIs502(t *testing.T) {
	var n atomic.Int32
	h := relayHub(t, []string{CapabilityRelay}, func(context.Context, *Session, RelayRequest) RelayResponse {
		if n.Add(1) == 1 {
			panic("boom")
		}
		return RelayResponse{Status: 200, Body: []byte(`{"ok":true}`)}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	r, err := cs.Relay(context.Background(), RelayMessagePath, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadGateway, r.Status)
	assert.JSONEq(t, `{"ok":false,"error":"target_error"}`, string(r.Body))
	r, err = cs.Relay(context.Background(), RelayMessagePath, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, r.Status, "the tunnel still works")
}

// "Exactly the header": a relay header with any extra key is closed unanswered.
func TestRelayHeaderWithAnExtraKeyIsRefused(t *testing.T) {
	var calls atomic.Int32
	h := relayHub(t, []string{CapabilityRelay}, func(context.Context, *Session, RelayRequest) RelayResponse {
		calls.Add(1)
		return RelayResponse{Status: 200}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	st, err := cs.ys.OpenStream()
	require.NoError(t, err)
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(10 * time.Second))
	require.NoError(t, writeRawFrame(st, []byte(`{"kind":"relay","method":"POST","path":"/relay/v1/message","content_length":2,"source":"other-server"}`)))
	require.NoError(t, writeRawFrame(st, []byte(`{}`)))
	_, err = readFrame(st, 1<<16)
	assert.Error(t, err, "closed without an answer")
	assert.Equal(t, int32(0), calls.Load())
}

// A tunnel that drops during a relay request is a disconnect, not "the hub doesn't relay".
func TestRelayDisconnectIsNotUnsupported(t *testing.T) {
	entered := make(chan struct{})
	h := relayHub(t, []string{CapabilityRelay}, func(ctx context.Context, _ *Session, _ RelayRequest) RelayResponse {
		close(entered)
		<-ctx.Done()
		return RelayResponse{Status: 200}
	})
	sess, cs := connect(t, h, noopLocal(), nil)
	errc := make(chan error, 1)
	go func() {
		_, err := cs.Relay(context.Background(), RelayMessagePath, []byte(`{}`))
		errc <- err
	}()
	<-entered
	_ = sess.Close()
	err := <-errc
	assert.ErrorIs(t, err, ErrSessionClosed)
	assert.NotErrorIs(t, err, ErrRelayUnsupported)
}
