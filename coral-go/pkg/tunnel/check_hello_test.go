package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckHelloUnsupportedVersionAnswersUnsupportedVersion(t *testing.T) {
	var calls atomic.Int32
	var gotID, gotVer, gotTok atomic.Value
	cfg := fastCfg()
	cfg.CheckHello = func(h Hello, id any) error {
		calls.Add(1)
		gotID.Store(id)
		gotVer.Store(h.CoralVersion)
		gotTok.Store(h.ServerToken)
		return fmt.Errorf("coral %s too old: %w", h.CoralVersion, ErrUnsupportedVersion)
	}
	hub := newHub(t, cfg)

	w, c := rawHandshake(t, hub, Hello{ProtocolVersion: ProtocolVersion, ServerToken: testToken, CoralVersion: "v0.1"})
	defer c.CloseNow()
	assert.Equal(t, CodeUnsupportedVersion, w.Error)
	assert.Zero(t, w.AcceptedVersion)
	assert.EqualValues(t, 1, calls.Load())
	assert.Equal(t, "server-1", gotID.Load(), "the hook gets the authenticated identity")
	assert.Equal(t, "v0.1", gotVer.Load())
	assert.Equal(t, "", gotTok.Load(), "the hook never sees the server token")
	select {
	case <-hub.Sessions:
		t.Fatal("a refused hello got a session")
	default:
	}
}

// Only the typed ErrUnsupportedVersion is answered unsupported_version; any other error from the hook is answered like a failed auth.
func TestCheckHelloOtherErrorAnswersUnauthorized(t *testing.T) {
	cfg := fastCfg()
	cfg.CheckHello = func(Hello, any) error { return errors.New("some lookup failed") }
	hub := newHub(t, cfg)
	w, c := rawHandshake(t, hub, Hello{ProtocolVersion: ProtocolVersion, ServerToken: testToken})
	defer c.CloseNow()
	assert.Equal(t, CodeUnauthorized, w.Error)
	select {
	case <-hub.Sessions:
		t.Fatal("a refused hello got a session")
	default:
	}
}

func TestCheckHelloNeverRunsForAnUnauthenticatedPeer(t *testing.T) {
	var calls atomic.Int32
	cfg := fastCfg()
	cfg.CheckHello = func(Hello, any) error { calls.Add(1); return ErrUnsupportedVersion }
	hub := newHub(t, cfg)

	w, c := rawHandshake(t, hub, Hello{ProtocolVersion: ProtocolVersion, ServerToken: "wrong"})
	defer c.CloseNow()
	assert.Equal(t, CodeUnauthorized, w.Error)
	assert.Zero(t, calls.Load(), "the hook must not run before auth succeeds")
}

// rawWelcomeBytes sends hello and returns the hub's first reply byte for byte.
func rawWelcomeBytes(t *testing.T, hub *hubHarness, hello Hello) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, hub.URL(), nil)
	require.NoError(t, err)
	defer c.CloseNow()
	b, _ := json.Marshal(hello)
	require.NoError(t, c.Write(ctx, websocket.MessageBinary, b))
	_, msg, err := c.Read(ctx)
	require.NoError(t, err)
	return msg
}

// An unauthenticated peer's answer is byte-identical with and without the hook, for a bad token and for a token and version that would both be refused.
func TestCheckHelloLeavesAnUnauthenticatedAnswerByteIdentical(t *testing.T) {
	with := fastCfg()
	with.CheckHello = func(Hello, any) error { return ErrUnsupportedVersion }
	hubWith, hubWithout := newHub(t, with), newHub(t, fastCfg())
	for _, h := range []Hello{
		{ProtocolVersion: ProtocolVersion, ServerToken: "wrong", CoralVersion: "v0.1"},
		{ProtocolVersion: 0, ServerToken: "wrong"},
	} {
		a, b := rawWelcomeBytes(t, hubWith, h), rawWelcomeBytes(t, hubWithout, h)
		assert.Equal(t, string(b), string(a))
		assert.Contains(t, string(a), CodeUnauthorized)
	}
}

func TestCheckHelloAcceptAndNilHookAreUnchanged(t *testing.T) {
	var calls atomic.Int32
	cfg := fastCfg()
	cfg.CheckHello = func(Hello, any) error { calls.Add(1); return nil }
	hub := newHub(t, cfg)
	w, c := rawHandshake(t, hub, Hello{ProtocolVersion: ProtocolVersion, ServerToken: testToken})
	require.Empty(t, w.Error)
	assert.Equal(t, ProtocolVersion, w.AcceptedVersion)
	assert.EqualValues(t, 1, calls.Load())
	c.CloseNow()
	hub.next(t)

	nilHub := newHub(t, fastCfg())
	w, c = rawHandshake(t, nilHub, Hello{ProtocolVersion: ProtocolVersion, ServerToken: testToken})
	require.Empty(t, w.Error)
	assert.Equal(t, ProtocolVersion, w.AcceptedVersion)
	c.CloseNow()
	nilHub.next(t)
}

// The hook's errors are answered as an AuthFunc's are: retry_later with the wait it asked for (also wrapped), unsupported_version, anything else unauthorized.
func TestCheckHelloErrorsAreAnsweredAsAnAuthFuncsAre(t *testing.T) {
	for name, c := range map[string]struct {
		err       error
		code      string
		retryWait int
	}{
		"retry later with a wait":          {RetryLater(42 * time.Second), CodeRetryLater, 42},
		"retry later wrapped":              {fmt.Errorf("account busy: %w", RetryLater(7*time.Second)), CodeRetryLater, 7},
		"the plain retry later":            {ErrRetryLater, CodeRetryLater, 0},
		"unsupported version":              {ErrUnsupportedVersion, CodeUnsupportedVersion, 0},
		"unsupported version wrapped":      {fmt.Errorf("too old: %w", ErrUnsupportedVersion), CodeUnsupportedVersion, 0},
		"any other error":                  {errors.New("lookup failed"), CodeUnauthorized, 0},
		"the unauthorized error itself":    {ErrUnauthorized, CodeUnauthorized, 0},
		"a wait too long is capped":        {RetryLater(time.Hour), CodeRetryLater, int(MaxRetryAfter / time.Second)},
		"a wait under a second is no wait": {RetryLater(500 * time.Millisecond), CodeRetryLater, 0},
	} {
		cfg := fastCfg()
		cfg.CheckHello = func(Hello, any) error { return c.err }
		hub := newHub(t, cfg)
		w, conn := rawHandshake(t, hub, Hello{ProtocolVersion: ProtocolVersion, ServerToken: testToken})
		assert.Equal(t, c.code, w.Error, name)
		assert.Equal(t, c.retryWait, w.RetryAfter, name)
		conn.CloseNow()
		select {
		case <-hub.Sessions:
			t.Fatal(name + ": a refused hello got a session")
		default:
		}
	}
}

// The hook is handed a copy: editing its Capabilities does not change what the handshake goes on to read.
func TestCheckHelloCannotChangeWhatTheHandshakeReads(t *testing.T) {
	cfg := fastCfg()
	cfg.CheckHello = func(h Hello, _ any) error {
		require.NotEmpty(t, h.Capabilities)
		h.Capabilities[0] = "edited/9"
		return nil
	}
	hub := newHub(t, cfg)
	w, c := rawHandshake(t, hub, Hello{ProtocolVersion: ProtocolVersion, ServerToken: testToken, Capabilities: []string{CapCrossTeamAllowlist}})
	defer c.CloseNow()
	require.Empty(t, w.Error)
	s := hub.next(t)
	assert.Equal(t, []string{CapCrossTeamAllowlist}, s.peerCaps)
}
