package tunnel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/redact"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4378 (C0): streamed artifact requests, the second connector-opened relay request kind.

func artifactHub(t *testing.T, cfg SessionConfig, on RelayArtifactFunc) *hubHarness {
	t.Helper()
	cfg.OnRelayArtifact = on
	if cfg.Capabilities == nil {
		cfg.Capabilities = func(any) []string { return []string{CapabilityRelay, CapabilityRelayArtifact} }
	}
	if cfg.OnRelay == nil {
		cfg.OnRelay = func(_ context.Context, _ *Session, req RelayRequest) RelayResponse {
			return RelayResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}
		}
	}
	return newHub(t, cfg)
}

func okBody() []byte { return []byte(`{"ok":true}`) }

// patternReader yields n bytes of a repeating pattern without ever holding them, and counts what it gave.
type patternReader struct {
	n, given atomic.Int64
	limit    int64
}

func newPattern(n int64) *patternReader { return &patternReader{limit: n} }

func (p *patternReader) Read(b []byte) (int, error) {
	left := p.limit - p.given.Load()
	if left <= 0 {
		return 0, io.EOF
	}
	if int64(len(b)) > left {
		b = b[:left]
	}
	base := p.given.Load()
	for i := range b {
		b[i] = byte((base + int64(i)) % 251)
	}
	p.given.Add(int64(len(b)))
	return len(b), nil
}

// The body reaches the hook as an io.Reader with exactly the declared bytes, the answer comes back,
// and the source is the tunnel.
func TestRelayArtifactRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("artifact bytes "), 40_000) // 600 KB
	var gotSum [32]byte
	var gotLen int64
	h := artifactHub(t, SessionConfig{}, func(ctx context.Context, s *Session, req RelayArtifactRequest) RelayResponse {
		assert.Equal(t, "server-1", s.Identity())
		assert.Equal(t, RelayArtifactPath, req.Path)
		assert.EqualValues(t, len(payload), req.ContentLength)
		hash := sha256.New()
		n, err := io.Copy(hash, req.Body)
		assert.NoError(t, err)
		copy(gotSum[:], hash.Sum(nil))
		gotLen = n
		assert.False(t, req.Trailing(), "nothing past the declared length")
		return RelayResponse{Status: http.StatusOK, Body: okBody()}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	require.True(t, cs.CanRelayArtifact())
	resp, err := cs.RelayArtifact(context.Background(), int64(len(payload)), bytes.NewReader(payload))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.Status)
	assert.JSONEq(t, `{"ok":true}`, string(resp.Body))
	assert.EqualValues(t, len(payload), gotLen)
	assert.Equal(t, sha256.Sum256(payload), gotSum)
}

// The body is streamed: the hub's hook is reading while the sender still has most of the body to give,
// and the sender never holds more than a chunk.
func TestRelayArtifactStreamsWithoutBufferingTheBody(t *testing.T) {
	const n = 10 << 20
	src := newPattern(n)
	var givenWhenHalfRead atomic.Int64
	h := artifactHub(t, SessionConfig{MaxArtifactBytes: n + 8192}, func(ctx context.Context, s *Session, req RelayArtifactRequest) RelayResponse {
		buf := make([]byte, 32<<10)
		var read int64
		for {
			m, err := req.Body.Read(buf)
			read += int64(m)
			if read >= n/2 && givenWhenHalfRead.Load() == 0 {
				givenWhenHalfRead.Store(src.given.Load())
			}
			if err != nil {
				assert.ErrorIs(t, err, io.EOF)
				break
			}
		}
		assert.EqualValues(t, n, read)
		return RelayResponse{Status: http.StatusOK, Body: okBody()}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	resp, err := cs.RelayArtifact(context.Background(), n, src)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.Status)
	// When the hub had read half, the sender had given at most half plus what is in flight (a few windows):
	// a buffered body would have been given whole before the hub saw a byte.
	assert.Less(t, givenWhenHalfRead.Load(), int64(n)*9/10, "the sender was still sending when the hub was reading")
}

// The size is checked BEFORE a byte is read: n of 0 or over the cap is refused (locally by the connector,
// and by the hub for a crafted stream), the hook is never called, and a message still works.
func TestRelayArtifactCaps(t *testing.T) {
	var calls atomic.Int32
	h := artifactHub(t, SessionConfig{MaxArtifactBytes: 1 << 20}, func(context.Context, *Session, RelayArtifactRequest) RelayResponse {
		calls.Add(1)
		return RelayResponse{Status: http.StatusOK, Body: okBody()}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	for _, n := range []int64{0, -1, RelayMaxArtifact + 1} {
		_, err := cs.RelayArtifact(context.Background(), n, bytes.NewReader(nil))
		require.Error(t, err, "n=%d", n)
	}
	// A crafted request over the hub's cap (1 MiB here) is closed unanswered: no hook, no read.
	st, err := cs.ys.OpenStream()
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, writeFrame(st, Header{Kind: KindRelay, Method: http.MethodPost, Path: RelayArtifactPath, ContentLength: 1<<20 + 1}))
	_ = st.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = readFrame(st, 1<<10)
	assert.True(t, errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF), "closed unanswered: %v", err)
	assert.Zero(t, calls.Load())

	// Headers, a query, a bad method or a wrong kind are closed too.
	for _, hd := range []Header{
		{Kind: KindRelay, Method: http.MethodPost, Path: RelayArtifactPath, ContentLength: 10, Query: "x=1"},
		{Kind: KindRelay, Method: http.MethodGet, Path: RelayArtifactPath, ContentLength: 10},
		{Kind: KindRelay, Method: http.MethodPost, Path: RelayArtifactPath, ContentLength: 10, Headers: http.Header{"X-A": {"b"}}},
		{Kind: KindRelay, Method: http.MethodPost, Path: RelayArtifactPath, ContentLength: 0},
	} {
		st2, err := cs.ys.OpenStream()
		require.NoError(t, err)
		require.NoError(t, writeFrame(st2, hd))
		_ = st2.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err = readFrame(st2, 1<<10)
		assert.True(t, errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF), "%+v closed unanswered: %v", hd, err)
		st2.Close()
	}
	assert.Zero(t, calls.Load())

	// A message is unaffected (its own path, its own cap).
	resp, err := cs.Relay(context.Background(), RelayMessagePath, []byte(`{"message_id":"m"}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.Status)
	_, err = cs.Relay(context.Background(), RelayMessagePath, bytes.Repeat([]byte("x"), RelayMaxMessage+1))
	assert.Error(t, err, "a message keeps its 64 KiB cap")
}

// The default cap is 8 MiB plus the frame, and MaxArtifactBytes raises it only up to the ceiling.
func TestArtifactCapDefaults(t *testing.T) {
	assert.EqualValues(t, RelayDefaultArtifact, SessionConfig{}.withDefaults().MaxArtifactBytes)
	assert.Greater(t, int64(RelayDefaultArtifact), int64(8<<20+4+4096))
	assert.Less(t, int64(RelayDefaultArtifact), int64(8<<20+4+4096+1024))
	assert.EqualValues(t, 12<<20, SessionConfig{MaxArtifactBytes: 12 << 20}.withDefaults().MaxArtifactBytes)
	assert.EqualValues(t, RelayMaxArtifact, SessionConfig{MaxArtifactBytes: 1 << 40}.withDefaults().MaxArtifactBytes, "never past the ceiling")
}

// Default-deny on both ends: a hub that does not choose or have the hook never advertises it and the
// connector never opens a stream; an old hub (no capability) is the same; a crafted stream to a hub without the hook is closed.
func TestRelayArtifactDefaultDeny(t *testing.T) {
	var calls atomic.Int32
	hook := func(context.Context, *Session, RelayArtifactRequest) RelayResponse {
		calls.Add(1)
		return RelayResponse{Status: 200, Body: okBody()}
	}
	for name, h := range map[string]*hubHarness{
		"no hook":               newHub(t, SessionConfig{Capabilities: func(any) []string { return []string{CapabilityRelay, CapabilityRelayArtifact} }, OnRelay: func(context.Context, *Session, RelayRequest) RelayResponse { return RelayResponse{Status: 200} }}),
		"capability not chosen": newHub(t, SessionConfig{OnRelayArtifact: hook, Capabilities: func(any) []string { return []string{CapabilityRelay} }, OnRelay: func(context.Context, *Session, RelayRequest) RelayResponse { return RelayResponse{Status: 200} }}),
		"no capabilities":       newHub(t, SessionConfig{OnRelayArtifact: hook}),
	} {
		t.Run(name, func(t *testing.T) {
			_, cs := connect(t, h, noopLocal(), nil)
			assert.False(t, cs.CanRelayArtifact())
			_, err := cs.RelayArtifact(context.Background(), 10, bytes.NewReader(make([]byte, 10)))
			assert.ErrorIs(t, err, ErrRelayArtifactUnsupported)
			// A connector that opens it anyway (a bug or an attacker) gets a closed stream, and no hook runs.
			st, err := cs.ys.OpenStream()
			require.NoError(t, err)
			defer st.Close()
			require.NoError(t, writeFrame(st, Header{Kind: KindRelay, Method: http.MethodPost, Path: RelayArtifactPath, ContentLength: 10}))
			_, _ = st.Write(make([]byte, 10))
			_ = st.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, err = readFrame(st, 1<<10)
			assert.True(t, errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF), "closed unanswered: %v", err)
		})
	}
	assert.Zero(t, calls.Load())
}

// A body that is shorter or longer than declared fails the call; the hub sees a short body or nothing past n.
func TestRelayArtifactLengthIsExact(t *testing.T) {
	var gotErr atomic.Value
	var trailing atomic.Bool
	h := artifactHub(t, SessionConfig{}, func(ctx context.Context, s *Session, req RelayArtifactRequest) RelayResponse {
		_, err := io.Copy(io.Discard, req.Body)
		if err != nil {
			gotErr.Store(err)
		}
		trailing.Store(req.Trailing())
		return RelayResponse{Status: http.StatusOK, Body: okBody()}
	})
	_, cs := connect(t, h, noopLocal(), nil)

	_, err := cs.RelayArtifact(context.Background(), 1000, bytes.NewReader(make([]byte, 600)))
	require.ErrorIs(t, err, ErrArtifactLength, "fewer bytes than declared")
	require.Eventually(t, func() bool { return gotErr.Load() != nil }, 3*time.Second, 10*time.Millisecond)
	assert.ErrorIs(t, gotErr.Load().(error), io.ErrUnexpectedEOF, "the hub sees a short body")

	_, err = cs.RelayArtifact(context.Background(), 1000, bytes.NewReader(make([]byte, 1500)))
	require.ErrorIs(t, err, ErrArtifactLength, "more bytes than declared")
}

// An overrun from a hostile connector: the hub reads exactly n and Trailing reports the extra byte.
func TestRelayArtifactTrailingByteIsDetected(t *testing.T) {
	var trailing atomic.Bool
	done := make(chan struct{})
	h := artifactHub(t, SessionConfig{}, func(ctx context.Context, s *Session, req RelayArtifactRequest) RelayResponse {
		b, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Len(t, b, 100, "exactly the declared length is read")
		trailing.Store(req.Trailing())
		close(done)
		return RelayResponse{Status: http.StatusOK, Body: okBody()}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	st, err := cs.ys.OpenStream()
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, writeFrame(st, Header{Kind: KindRelay, Method: http.MethodPost, Path: RelayArtifactPath, ContentLength: 100}))
	_, err = st.Write(make([]byte, 101)) // one byte too many
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the hook did not finish")
	}
	assert.True(t, trailing.Load(), "the byte past the declared length is reported")
}

// A request is aborted on both streams when there is no progress for the idle time: a SENDER that goes quiet,
// and a HOOK that stops reading (a stalled target it is writing to).
func TestRelayArtifactIdleDeadline(t *testing.T) {
	old := artifactIdleFor
	artifactIdleFor = 300 * time.Millisecond
	t.Cleanup(func() { artifactIdleFor = old })

	t.Run("sender goes quiet", func(t *testing.T) {
		hookErr := make(chan error, 1)
		h := artifactHub(t, SessionConfig{}, func(ctx context.Context, s *Session, req RelayArtifactRequest) RelayResponse {
			_, err := io.Copy(io.Discard, req.Body)
			hookErr <- err
			return RelayResponse{Status: http.StatusGatewayTimeout, Body: okBody()}
		})
		_, cs := connect(t, h, noopLocal(), nil)
		pr, pw := io.Pipe()
		go func() { pw.Write(make([]byte, 1000)) /* then silence */ }()
		t.Cleanup(func() { pw.Close() })
		begin := time.Now()
		_, err := cs.RelayArtifact(context.Background(), 5000, pr)
		require.Error(t, err)
		assert.Less(t, time.Since(begin), 5*time.Second, "ended by the idle deadline, not the total")
		select {
		case e := <-hookErr:
			assert.Error(t, e, "the hook's read failed")
		case <-time.After(5 * time.Second):
			t.Fatal("the hook was not released")
		}
	})

	t.Run("hook stops reading", func(t *testing.T) {
		ctxDone := make(chan struct{})
		h := artifactHub(t, SessionConfig{}, func(ctx context.Context, s *Session, req RelayArtifactRequest) RelayResponse {
			var one [1024]byte
			_, _ = req.Body.Read(one[:]) // reads a little, then stalls writing to a target that does not answer
			select {
			case <-ctx.Done(): // the hub aborts: the target's stream is closed through ctx
				close(ctxDone)
			case <-time.After(10 * time.Second):
			}
			return RelayResponse{Status: http.StatusGatewayTimeout, Body: okBody()}
		})
		_, cs := connect(t, h, noopLocal(), nil)
		src := newPattern(4 << 20) // more than the stream window, so the sender blocks too
		begin := time.Now()
		_, err := cs.RelayArtifact(context.Background(), 4<<20, src)
		require.Error(t, err)
		assert.Less(t, time.Since(begin), 8*time.Second)
		select {
		case <-ctxDone:
		case <-time.After(5 * time.Second):
			t.Fatal("the hub did not cancel the hook's context: the target's stream would stay open")
		}
	})
}

// 120 s in all (shortened here): a sender that keeps trickling still ends at the total.
func TestRelayArtifactTotalDeadline(t *testing.T) {
	oldT, oldI := artifactTotalFor, artifactIdleFor
	artifactTotalFor, artifactIdleFor = 600*time.Millisecond, 2*time.Second
	t.Cleanup(func() { artifactTotalFor, artifactIdleFor = oldT, oldI })
	h := artifactHub(t, SessionConfig{}, func(ctx context.Context, s *Session, req RelayArtifactRequest) RelayResponse {
		_, _ = io.Copy(io.Discard, req.Body)
		return RelayResponse{Status: 200, Body: okBody()}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	pr, pw := io.Pipe()
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				pw.Write([]byte("x"))
			}
		}
	}()
	t.Cleanup(func() { close(stop); pw.Close() })
	begin := time.Now()
	_, err := cs.RelayArtifact(context.Background(), 1<<20, pr)
	require.Error(t, err)
	assert.Less(t, time.Since(begin), 3*time.Second)
}

// A hook that panics is the fixed 502; a hook budget over MaxArtifactInFlight is the fixed 429; neither echoes the request.
func TestRelayArtifactPanicAndBudget(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	started := 0
	h := artifactHub(t, SessionConfig{}, func(ctx context.Context, s *Session, req RelayArtifactRequest) RelayResponse {
		mu.Lock()
		started++
		n := started
		mu.Unlock()
		if n == 1 {
			panic("secret-body-text")
		}
		if n == 2 {
			<-release
		}
		return RelayResponse{Status: 200, Body: okBody()}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	resp, err := cs.RelayArtifact(context.Background(), 4, bytes.NewReader([]byte("abcd")))
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadGateway, resp.Status)
	assert.NotContains(t, string(resp.Body), "secret")

	// Hold one in flight; a second (the default budget is 1 per tunnel) is 429.
	held := make(chan struct{})
	go func() {
		defer close(held)
		cs.RelayArtifact(context.Background(), 4, bytes.NewReader([]byte("abcd")))
	}()
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return started == 2 }, 3*time.Second, 5*time.Millisecond)
	resp, err = cs.RelayArtifact(context.Background(), 4, bytes.NewReader([]byte("abcd")))
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, resp.Status)
	assert.Equal(t, 1, resp.RetryAfter)
	close(release)
	<-held
}

// shapedClient is connectShaped returning the connector's side too.
func shapedClient(t testing.TB, h *hubHarness, shape linkShape) (*Session, *ClientSession) {
	ls := httptest.NewServer(noopLocal())
	t.Cleanup(ls.Close)
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return newShapedConn(c, shape.mbit*1e6/8, shape.rtt/2, shape.queue), nil
	}}
	cs, err := Dial(context.Background(), h.URL(), testToken, ClientOptions{
		SessionConfig: h.Cfg, Handler: &LocalHandler{BaseURL: ls.URL, AllowPath: allowAll, Redactor: redact.Rules},
		CoralVersion: "test", AllowInsecure: true, HTTPClient: &http.Client{Transport: tr},
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return h.next(t), cs
}

// coral-hub's regression (H0 acceptance, #4378): while a bulk artifact upload runs on the same tunnel, a
// Hub sign-in check and a relay message stay within budget. The upload is on the bulk lane, so the small
// requests overtake it on the throttled uplink (5 Mbit, 60 ms RTT, the hub's reproduction).
func TestRelayArtifactDoesNotDelaySigninOrMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("throttled-link measurement (~10 s)")
	}
	measure := func(t *testing.T) (p95 time.Duration, uploaded bool) {
		shape := hol
		shape.queue = sendLowWater
		const size = 2 << 20
		cfg := SessionConfig{
			Capabilities: func(any) []string {
				return []string{CapabilityRelay, CapabilityRelayArtifact, CapabilitySignin}
			},
			OnRelay: func(context.Context, *Session, RelayRequest) RelayResponse {
				return RelayResponse{Status: 200, Body: okBody()}
			},
			OnSignin: func(context.Context, *Session, SigninRequest) SigninResponse {
				return SigninResponse{Status: 200, Body: okBody()}
			},
			OnRelayArtifact: func(_ context.Context, _ *Session, req RelayArtifactRequest) RelayResponse {
				_, _ = io.Copy(io.Discard, req.Body)
				return RelayResponse{Status: 200, Body: okBody()}
			},
		}
		h := newHub(t, cfg)
		_, cs := shapedClient(t, h, shape)
		upDone := make(chan error, 1)
		go func() {
			_, err := cs.RelayArtifact(context.Background(), size, newPattern(size))
			upDone <- err
		}()
		time.Sleep(300 * time.Millisecond) // the upload is running and has filled the link
		var lat []time.Duration
		for i := 0; i < 16; i++ {
			begin := time.Now()
			var err error
			if i%2 == 0 {
				_, err = cs.Relay(context.Background(), RelayTargetsPath, []byte(`{"from_board":"qa"}`))
			} else {
				_, err = cs.Signin(context.Background(), SigninCheck, []byte(`{}`))
			}
			require.NoError(t, err)
			lat = append(lat, time.Since(begin))
			time.Sleep(100 * time.Millisecond)
		}
		select {
		case <-upDone:
			uploaded = true
		default: // still running: the requests overlapped it, which is the point
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		return lat[len(lat)*95/100], uploaded
	}
	p95, _ := measure(t)
	if p95 >= 2*hubCriterion {
		t.Logf("p95 %v missed %v with load average %s: measuring once more (#3716)", p95.Round(time.Millisecond), 2*hubCriterion, loadavg())
		p95, _ = measure(t)
	}
	t.Logf("sign-in and relay-message p95 beside a 2 MiB artifact upload: %v", p95.Round(time.Millisecond))
	assert.Less(t, p95, 2*hubCriterion, "a sign-in check or a relay message stays within budget beside a bulk artifact upload")
}

// The lane a connector-opened request is written on: the artifact path is bulk, every other request
// (messages, targets, sign-in) interactive. Pinned apart from the latency test above, which also passes
// when the artifact is interactive on a link this small.
func TestInboundLane(t *testing.T) {
	assert.Equal(t, laneBulk, inboundLane(RelayArtifactPath))
	for _, p := range []string{RelayMessagePath, RelayTargetsPath, signinPathPrefix + SigninCheck, signinPathPrefix + SigninBegin} {
		assert.Equal(t, laneInteractive, inboundLane(p), p)
	}
	assert.Equal(t, laneBulk, artifactLane, "the artifact request's lane is bulk by default")
}

// A message path and the artifact path never mix: the message kinds do not accept the artifact path, and a
// message-sized body to the artifact path is not a message.
func TestRelayMessageKindsDoNotAcceptTheArtifactPath(t *testing.T) {
	assert.Zero(t, relayCap(RelayArtifactPath))
	assert.Zero(t, signinCap(RelayArtifactPath))
	assert.False(t, validInboundHeader(relayKind, Header{Kind: KindRelay, Method: http.MethodPost, Path: RelayArtifactPath, ContentLength: 10}))
}
