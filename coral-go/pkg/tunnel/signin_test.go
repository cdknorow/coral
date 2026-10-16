package tunnel

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3966: Hub sign-in, the second connector-opened kind, on the relay's wire.

func signinHub(t *testing.T, caps []string, on SigninFunc, relay RelayFunc) *hubHarness {
	t.Helper()
	cfg := SessionConfig{OnSignin: on, OnRelay: relay}
	if caps != nil {
		cfg.Capabilities = func(any) []string { return caps }
	}
	return newHub(t, cfg)
}

func okSignin(context.Context, *Session, SigninRequest) SigninResponse {
	return SigninResponse{Status: 200, Body: []byte(`{"ok":true}`)}
}

// Each operation reaches OnSignin with its name and body, on the tunnel it came from, and
// the hub's status, body and Retry-After come back.
func TestSigninRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var got []SigninRequest
	h := signinHub(t, []string{CapabilitySignin}, func(_ context.Context, s *Session, req SigninRequest) SigninResponse {
		assert.Equal(t, "server-1", s.Identity(), "the machine is the tunnel, never the body")
		mu.Lock()
		got = append(got, req)
		mu.Unlock()
		if req.Op == SigninBegin {
			return SigninResponse{Status: 429, Body: []byte(`{"ok":false}`), RetryAfter: 5}
		}
		return SigninResponse{Status: 200, Body: []byte(`{"ok":true,"op":"` + req.Op + `"}`)}
	}, nil)
	_, cs := connect(t, h, noopLocal(), nil)
	require.True(t, cs.CanSignin())
	assert.False(t, cs.CanRelay(), "only what was advertised")
	for _, op := range []string{SigninOrigins, SigninRedeem, SigninCheck} {
		r, err := cs.Signin(context.Background(), op, []byte(`{"v":1}`))
		require.NoError(t, err, op)
		assert.Equal(t, 200, r.Status)
		assert.JSONEq(t, `{"ok":true,"op":"`+op+`"}`, string(r.Body))
	}
	r, err := cs.Signin(context.Background(), SigninBegin, []byte(`{"v":1}`))
	require.NoError(t, err)
	assert.Equal(t, 429, r.Status)
	assert.Equal(t, 5, r.RetryAfter)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 4)
	assert.Equal(t, SigninBegin, got[3].Op)
	assert.Equal(t, `{"v":1}`, string(got[3].Body))
}

// Sign-in is advertised only when the hub decides so AND has OnSignin; otherwise the
// connector never opens a sign-in stream.
func TestSigninNotAdvertisedWithoutTheHubsYes(t *testing.T) {
	for name, h := range map[string]*hubHarness{
		"no capabilities hook": signinHub(t, nil, okSignin, nil),
		"hook says relay only": signinHub(t, []string{CapabilityRelay}, okSignin, nil),
		"no OnSignin":          signinHub(t, []string{CapabilitySignin}, nil, nil),
	} {
		_, cs := connect(t, h, noopLocal(), nil)
		assert.False(t, cs.CanSignin(), name)
		_, err := cs.Signin(context.Background(), SigninCheck, []byte(`{}`))
		assert.ErrorIs(t, err, ErrSigninUnsupported, name)
	}
}

// A kind the hub didn't advertise to this tunnel is closed unanswered even when the hub has
// its hook, and only advertised kinds' hooks run.
func TestInboundServesOnlyAdvertisedKinds(t *testing.T) {
	var signins, relays atomic.Int32
	h := signinHub(t, []string{CapabilityRelay}, func(context.Context, *Session, SigninRequest) SigninResponse {
		signins.Add(1)
		return SigninResponse{Status: 200}
	}, func(context.Context, *Session, RelayRequest) RelayResponse {
		relays.Add(1)
		return RelayResponse{Status: 200}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	st, err := cs.ys.OpenStream()
	require.NoError(t, err)
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(10 * time.Second))
	require.NoError(t, writeFrame(st, Header{Kind: KindSignin, Method: "POST", Path: "/signin/v1/check", ContentLength: 2}))
	_ = writeRawFrame(st, []byte(`{}`))
	_, err = readFrame(st, 1<<16)
	assert.Error(t, err, "closed without an answer")
	_, err = cs.Relay(context.Background(), RelayTargetsPath, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, int32(0), signins.Load())
	assert.Equal(t, int32(1), relays.Load())
}

// Anything but exactly a sign-in request is closed unanswered and OnSignin never runs.
func TestSigninClosesEveryOtherStream(t *testing.T) {
	var calls atomic.Int32
	h := signinHub(t, []string{CapabilitySignin}, func(context.Context, *Session, SigninRequest) SigninResponse {
		calls.Add(1)
		return SigninResponse{Status: 200}
	}, nil)
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
	big := []byte(strings.Repeat("x", SigninMaxBody+1))
	for name, tc := range map[string]struct {
		hdr  Header
		body []byte
	}{
		"unknown op":      {Header{Kind: KindSignin, Method: "POST", Path: "/signin/v1/admin", ContentLength: 2}, small},
		"other version":   {Header{Kind: KindSignin, Method: "POST", Path: "/signin/v2/check", ContentLength: 2}, small},
		"relay path":      {Header{Kind: KindSignin, Method: "POST", Path: RelayMessagePath, ContentLength: 2}, small},
		"GET":             {Header{Kind: KindSignin, Method: "GET", Path: "/signin/v1/check", ContentLength: 2}, small},
		"query":           {Header{Kind: KindSignin, Method: "POST", Path: "/signin/v1/check", Query: "a=1", ContentLength: 2}, small},
		"headers":         {Header{Kind: KindSignin, Method: "POST", Path: "/signin/v1/check", Headers: http.Header{"X-Coral-Hub-User": {"hub-user-0000000a"}}, ContentLength: 2}, small},
		"a hub user":      {Header{Kind: KindSignin, Method: "POST", Path: "/signin/v1/check", HubUser: "hub-user-0000000a", ContentLength: 2}, small},
		"a hub tier":      {Header{Kind: KindSignin, Method: "POST", Path: "/signin/v1/check", HubTier: "launch", ContentLength: 2}, small},
		"over the cap":    {Header{Kind: KindSignin, Method: "POST", Path: "/signin/v1/redeem", ContentLength: SigninMaxBody + 1}, big},
		"short body":      {Header{Kind: KindSignin, Method: "POST", Path: "/signin/v1/check", ContentLength: 10}, small},
		"relay kind here": {Header{Kind: KindRelay, Method: "POST", Path: RelayMessagePath, ContentLength: 2}, small},
	} {
		assert.Error(t, send(tc.hdr, tc.body), "%s: closed without an answer", name)
	}
	assert.Equal(t, int32(0), calls.Load(), "OnSignin never ran")
}

// A panic in OnSignin is the fixed {"ok":false} with 502, and the tunnel keeps working; a
// reply over the cap is the same.
func TestSigninHandlerPanicAndOversizeAre502(t *testing.T) {
	var n atomic.Int32
	h := signinHub(t, []string{CapabilitySignin}, func(context.Context, *Session, SigninRequest) SigninResponse {
		switch n.Add(1) {
		case 1:
			panic("boom")
		case 2:
			return SigninResponse{Status: 200, Body: []byte(strings.Repeat("x", SigninMaxBody+1))}
		}
		return SigninResponse{Status: 200, Body: []byte(`{"ok":true}`)}
	}, nil)
	_, cs := connect(t, h, noopLocal(), nil)
	for i := 0; i < 2; i++ {
		r, err := cs.Signin(context.Background(), SigninCheck, []byte(`{}`))
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadGateway, r.Status)
		assert.JSONEq(t, `{"ok":false}`, string(r.Body))
	}
	r, err := cs.Signin(context.Background(), SigninCheck, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, 200, r.Status, "the tunnel still works")
}

// The connector refuses an unknown operation and an oversized body before opening a stream.
func TestSigninClientChecks(t *testing.T) {
	var calls atomic.Int32
	h := signinHub(t, []string{CapabilitySignin}, func(context.Context, *Session, SigninRequest) SigninResponse {
		calls.Add(1)
		return SigninResponse{Status: 200}
	}, nil)
	_, cs := connect(t, h, noopLocal(), nil)
	_, err := cs.Signin(context.Background(), "admin", []byte(`{}`))
	assert.Error(t, err)
	_, err = cs.Signin(context.Background(), "../check", []byte(`{}`))
	assert.Error(t, err)
	_, err = cs.Signin(context.Background(), SigninRedeem, make([]byte, SigninMaxBody+1))
	assert.Error(t, err)
	assert.Equal(t, int32(0), calls.Load())
}

// #3966: sign-in has its own in-flight budget (2) beside the relay's (4): a relay burst at its
// full budget doesn't make a check wait or come back busy, and a third concurrent sign-in call is
// answered 429 with the fixed body.
func TestSigninHasItsOwnInFlightBudget(t *testing.T) {
	release := make(chan struct{})
	var relaysIn, signinsIn atomic.Int32
	h := signinHub(t, []string{CapabilitySignin, CapabilityRelay}, func(ctx context.Context, _ *Session, req SigninRequest) SigninResponse {
		if req.Op == SigninBegin { // the slow ones that fill sign-in's budget
			signinsIn.Add(1)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return SigninResponse{Status: 200, Body: []byte(`{"ok":true}`)}
	}, func(ctx context.Context, _ *Session, _ RelayRequest) RelayResponse {
		relaysIn.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return RelayResponse{Status: 200, Body: []byte(`{}`)}
	})
	_, cs := connect(t, h, noopLocal(), nil)
	var wg sync.WaitGroup
	defer func() { close(release); wg.Wait() }()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = cs.Relay(context.Background(), RelayTargetsPath, []byte(`{}`)) }()
	}
	require.Eventually(t, func() bool { return relaysIn.Load() == 4 }, 5*time.Second, 10*time.Millisecond, "the relay budget is full")
	r, err := cs.Signin(context.Background(), SigninCheck, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, 200, r.Status, "a check isn't blocked by a relay burst")

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = cs.Signin(context.Background(), SigninBegin, []byte(`{}`)) }()
	}
	require.Eventually(t, func() bool { return signinsIn.Load() == 2 }, 5*time.Second, 10*time.Millisecond, "sign-in's budget is full")
	r, err = cs.Signin(context.Background(), SigninCheck, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, r.Status, "over sign-in's own budget")
	assert.JSONEq(t, `{"ok":false}`, string(r.Body))
	assert.Equal(t, 1, r.RetryAfter)
}
