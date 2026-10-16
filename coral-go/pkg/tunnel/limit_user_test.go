package tunnel

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	userA = "hub-user-0000000a"
	userB = "hub-user-0000000b"
)

// holdAs is hold for one Coral Hub user's requests.
func (rig *limitRig) holdAs(user, path string, n int) {
	rig.t.Helper()
	want := rig.inFlight.Load() + int32(n)
	for i := 0; i < n; i++ {
		go func() {
			req, _ := http.NewRequestWithContext(WithHubUser(context.Background(), user), "GET", "http://x"+path, nil)
			if resp, err := rig.sess.RoundTrip(req); err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	require.Eventually(rig.t, func() bool { return rig.inFlight.Load() == want }, 5*time.Second, 5*time.Millisecond)
}

func (rig *limitRig) getAs(user, path string) (*http.Response, string) {
	rig.t.Helper()
	req, _ := http.NewRequestWithContext(WithHubUser(context.Background(), user), "GET", "http://x"+path, nil)
	resp, err := rig.sess.RoundTrip(req)
	require.NoError(rig.t, err, path)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// #3642: one Hub user holds at most 16 of the 64 general slots; their 17th request is refused
// at once while another user's request goes through, and a user's slot comes back when used.
func TestOneHubUserCannotFillTheGeneralPool(t *testing.T) {
	rig := newLimitRig(t, HubLimits{})
	rig.holdAs(userA, "/api/hold/a", 16)

	resp, body := rig.getAs(userA, "/api/hold/a?now=1")
	assertBusy(t, resp, body, "user A's 17th request")
	assert.Contains(t, body, "you have too many", "the refusal says it is this user's share")

	resp, body = rig.getAs(userB, "/api/hold/b?now=1")
	assert.Equal(t, 200, resp.StatusCode, "another user's request passes: %s", body)
	resp, body = rig.get("/api/hold/c?now=1")
	assert.Equal(t, 200, resp.StatusCode, "a stream without a user (an old hub) is held only by the pools: %s", body)

	rig.free()
	require.Eventually(t, func() bool { return rig.inFlight.Load() == 0 }, 5*time.Second, 5*time.Millisecond)
	resp, _ = rig.getAs(userA, "/api/hold/a?now=1")
	assert.Equal(t, 200, resp.StatusCode, "user A's slots came back")
}

// One user holds at most 4 of the 16 long-lived slots (long polls, terminals) and 2 of the 4
// search slots.
func TestOneHubUserCannotFillTheLongOrSearchPool(t *testing.T) {
	rig := newLimitRig(t, HubLimits{})
	rig.holdAs(userA, "/api/board/p/waits/poll", 4)
	resp, body := rig.getAs(userA, "/api/board/p/peek?now=1")
	assertBusy(t, resp, body, "user A's fifth long-lived request")
	_, err := rig.sess.DialWS(WithHubUser(context.Background(), userA), "/ws/terminal/x", "", nil)
	var se *StreamError
	require.ErrorAs(t, err, &se, "user A's terminal over their long share")
	assert.Equal(t, CodeTooManyStreams, se.Code)
	resp, body = rig.getAs(userB, "/api/board/p/waits/poll?now=1")
	assert.Equal(t, 200, resp.StatusCode, "user B's long poll passes: %s", body)

	rig.holdAs(userA, "/api/board/p/messages/all", 2)
	resp, body = rig.getAs(userA, "/api/sessions/history/search?q=x&now=1")
	assertBusy(t, resp, body, "user A's third search")
	resp, body = rig.getAs(userB, "/api/board/p/messages/all?now=1")
	assert.Equal(t, 200, resp.StatusCode, "user B's search passes: %s", body)
	resp, body = rig.getAs(userA, "/api/sessions/live?now=1")
	assert.Equal(t, 200, resp.StatusCode, "user A's ordinary read still passes: %s", body)
}

// The pool sizes come from HubLimitsFunc for each request (the settings), so a change applies
// to the next request without reconnecting.
func TestHubPoolSizesApplyPerRequest(t *testing.T) {
	var global atomic.Int32
	global.Store(2)
	rig := newLimitRig(t, HubLimits{})
	rig.sess, _ = connect(t, newHub(t, fastCfg()), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("now") == "" {
			rig.inFlight.Add(1)
			defer rig.inFlight.Add(-1)
			select {
			case <-rig.release:
			case <-r.Context().Done():
				return
			}
		}
		w.Write([]byte("ok"))
	}), func(l *LocalHandler) {
		l.HubLimitsFunc = func() HubLimits { return HubLimits{Global: int(global.Load())} }
	})
	rig.hold("/api/hold/a", 2)
	resp, body := rig.get("/api/hold/b?now=1")
	assertBusy(t, resp, body, "the third request with a pool of 2")
	global.Store(3)
	resp, body = rig.get("/api/hold/b?now=1")
	assert.Equal(t, 200, resp.StatusCode, "the larger pool applies at once: %s", body)
}

func TestHubUserShares(t *testing.T) {
	var l HubLimits
	assert.Equal(t, []int{16, 4, 2}, []int{l.userCap(poolGlobal), l.userCap(poolLong), l.userCap(poolSearch)})
	l = HubLimits{Global: 8, Long: 2, Search: 1}
	assert.Equal(t, []int{2, 1, 1}, []int{l.userCap(poolGlobal), l.userCap(poolLong), l.userCap(poolSearch)}, "never less than 1")
	l = HubLimits{UserGlobal: 5}
	assert.Equal(t, 5, l.userCap(poolGlobal))
}

// A stream's hub user must be a Coral Hub user id; anything else is a bad header.
func TestHeaderHubUserIsValidated(t *testing.T) {
	for _, u := range []string{"x", "hub-user-0000000A", "hub-user-0000000a\n", strings.Repeat("a", 100)} {
		h := Header{Kind: KindHTTP, Method: "GET", Path: "/api/x", HubUser: u}
		assert.ErrorIs(t, h.validate(Limits{}.withDefaults()), ErrBadHeader, u)
		assert.Empty(t, hubUserFrom(WithHubUser(context.Background(), u)), "not carried: %q", u)
	}
	h := Header{Kind: KindHTTP, Method: "GET", Path: "/api/x", HubUser: userA}
	assert.NoError(t, h.validate(Limits{}.withDefaults()))
}

// #3642: a reply nobody reads frees its slot. The hub-side reader stops taking a large body;
// after BodyIdle without progress the connector ends the stream, the reply is truncated, and the
// one general slot is free again.
func TestStalledReaderFreesItsSlot(t *testing.T) {
	big := strings.Repeat("x", 8<<20) // far more than the stream's window
	rig := &limitRig{t: t, release: make(chan struct{})}
	rig.sess, _ = connect(t, newHub(t, fastCfg()), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/big" {
			w.Header().Set("Content-Type", "application/octet-stream")
			io.Copy(w, strings.NewReader(big))
			return
		}
		w.Write([]byte("ok"))
	}), func(l *LocalHandler) {
		l.HubLimits = HubLimits{Global: 1, BodyIdle: 300 * time.Millisecond}
	})

	req, _ := http.NewRequest("GET", "http://x/api/big", nil)
	stalled, err := rig.sess.RoundTrip(req)
	require.NoError(t, err)
	defer stalled.Body.Close()
	// Read a little, then stop reading.
	_, err = io.ReadFull(stalled.Body, make([]byte, 1024))
	require.NoError(t, err)

	resp, body := rig.get("/api/small")
	assertBusy(t, resp, body, "the one slot is held by the stalled reply")

	require.Eventually(t, func() bool {
		resp, _ := rig.get("/api/small")
		return resp.StatusCode == 200
	}, 5*time.Second, 50*time.Millisecond, "the stalled reply's slot is freed after BodyIdle")

	n, err := io.Copy(io.Discard, stalled.Body)
	assert.Error(t, err, "the stalled reply ends truncated, not complete")
	assert.Less(t, n+1024, int64(len(big)))
}

// A local reply that stops sending mid-body is ended after BodyIdle too, and frees its slot.
func TestStalledLocalBodyFreesItsSlot(t *testing.T) {
	rig := &limitRig{t: t, release: make(chan struct{})}
	t.Cleanup(rig.free)
	rig.sess, _ = connect(t, newHub(t, fastCfg()), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/trickle" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("first part"))
			w.(http.Flusher).Flush()
			select {
			case <-rig.release:
			case <-r.Context().Done():
			}
			return
		}
		w.Write([]byte("ok"))
	}), func(l *LocalHandler) {
		l.HubLimits = HubLimits{Global: 1, BodyIdle: 300 * time.Millisecond}
	})
	req, _ := http.NewRequest("GET", "http://x/api/trickle", nil)
	start := time.Now()
	resp, err := rig.sess.RoundTrip(req)
	require.NoError(t, err)
	type result struct {
		b   []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		b, err := io.ReadAll(resp.Body)
		done <- result{b, err}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		resp.Body.Close()
		t.Fatal("the stalled reply was never ended")
	}
	resp.Body.Close()
	assert.Error(t, got.err, "the reply ends truncated")
	assert.Equal(t, "first part", string(got.b))
	assert.Less(t, time.Since(start), 3*time.Second)
	resp, body := rig.get("/api/small")
	assert.Equal(t, 200, resp.StatusCode, "the slot was freed: %s", body)
}
