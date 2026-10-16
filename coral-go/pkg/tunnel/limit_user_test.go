package tunnel

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
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

// One user holds at most 32 of the 64 long-lived slots (long polls, terminals; #3663) and 2 of
// the 4 search slots.
func TestOneHubUserCannotFillTheLongOrSearchPool(t *testing.T) {
	rig := newLimitRig(t, HubLimits{})
	rig.holdAs(userA, "/api/board/p/waits/poll", 32)
	resp, body := rig.getAs(userA, "/api/board/p/peek?now=1")
	assertBusy(t, resp, body, "user A's 33rd long-lived request")
	_, err := rig.sess.DialWS(WithHubUser(context.Background(), userA), "/ws/terminal/x", "", nil)
	var se *StreamError
	require.ErrorAs(t, err, &se, "user A's terminal over their long share")
	assert.Equal(t, CodeTooManyStreams, se.Code)
	assert.Equal(t, BusyUser, se.Message, "the refusal says it is the user's own share (the hub maps it to its text)")
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
	assert.Equal(t, []int{16, 32, 2}, []int{l.userCap(poolGlobal), l.userCap(poolLong), l.userCap(poolSearch)})
	l = HubLimits{Global: 8, Long: 2, Search: 1}
	assert.Equal(t, []int{2, 2, 1}, []int{l.userCap(poolGlobal), l.userCap(poolLong), l.userCap(poolSearch)}, "a long pool under 16 is the user's whole")
	assert.Equal(t, 16, HubLimits{Long: 20}.userCap(poolLong), "at least 16 when the pool allows")
	assert.Equal(t, 50, HubLimits{Long: 100}.userCap(poolLong), "half a large pool")
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

// #3663: one user's normal use, a dashboard tab (its live feed) and 12 terminals, is never refused
// with the default limits; another user still gets in, and only past the user's share (32) does a
// terminal get the per-user refusal.
func TestATabAndTwelveTerminalsAreNeverRefused(t *testing.T) {
	sess, _ := connect(t, newHub(t, fastCfg()), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.Read(r.Context()) // held open until the test closes it
	}), nil)
	var open []*WSConn
	t.Cleanup(func() {
		for _, c := range open {
			c.CloseNow()
		}
	})
	dial := func(user, path string) error {
		c, err := sess.DialWS(WithHubUser(context.Background(), user), path, "", nil)
		if err == nil {
			open = append(open, c)
		}
		return err
	}
	require.NoError(t, dial(userA, "/ws/coral"), "the tab's live feed")
	for i := 0; i < 12; i++ {
		require.NoError(t, dial(userA, "/ws/terminal/agent-"+strconv.Itoa(i)), "terminal %d", i+1)
	}
	for i := 12; i < 31; i++ { // up to the share: 32 with the feed
		require.NoError(t, dial(userA, "/ws/terminal/agent-"+strconv.Itoa(i)), "terminal %d", i+1)
	}
	err := dial(userA, "/ws/terminal/one-too-many")
	var se *StreamError
	require.ErrorAs(t, err, &se, "past the user's share")
	assert.Equal(t, CodeTooManyStreams, se.Code)
	require.NoError(t, dial(userB, "/ws/coral"), "another user's tab still opens")
}

// #3663: users never crowd out the hub's own streams (its feed of this machine, its agent-list
// reads), which carry no user: users' streams leave the last slots of the pool to them.
func TestUsersLeaveTheHubsReserve(t *testing.T) {
	rig := newLimitRig(t, HubLimits{Long: 8}) // reserve 1; a user may take up to 7
	rig.holdAs(userA, "/api/board/p/waits/poll", 4)
	rig.holdAs(userB, "/api/board/p/waits/poll", 3)
	resp, body := rig.getAs(userB, "/api/board/p/peek?now=1")
	assertBusy(t, resp, body, "a user's stream into the hub's reserve")
	assert.NotContains(t, body, "you have too many", "it is the machine's limit, not the user's share")
	resp, body = rig.get("/api/board/p/peek?now=1")
	assert.Equal(t, 200, resp.StatusCode, "the hub's own (no user) gets the reserved slot: %s", body)

	assert.Equal(t, []int{4, 4, 0}, []int{HubLimits{}.reserve(poolLong), HubLimits{}.reserve(poolGlobal), HubLimits{}.reserve(poolSearch)})
	assert.Equal(t, 0, HubLimits{Long: 1}.reserve(poolLong), "a one-slot pool has no reserve")
}
