package tunnel

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// limitRig: a local API whose /api/hold/... routes (and the long-poll and search routes) block until
// released, behind a LocalHandler with small hub limits.
type limitRig struct {
	t        *testing.T
	sess     *Session
	base     string // the local API, as the dashboard reaches it directly
	inFlight atomic.Int32
	release  chan struct{}
	once     sync.Once
}

func newLimitRig(t *testing.T, lim HubLimits) *limitRig {
	t.Helper()
	rig := &limitRig{t: t, release: make(chan struct{})}
	hub := newHub(t, fastCfg())
	rig.sess, _ = connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/hold/") || strings.HasSuffix(r.URL.Path, "/waits/poll") || strings.HasSuffix(r.URL.Path, "/messages/all") {
			if r.URL.Query().Get("now") == "" {
				rig.inFlight.Add(1)
				defer rig.inFlight.Add(-1)
				select {
				case <-rig.release:
				case <-r.Context().Done():
					return
				}
			}
		}
		w.Write([]byte("ok"))
	}), func(l *LocalHandler) {
		l.HubLimits = lim
		rig.base = l.BaseURL
	})
	t.Cleanup(rig.free)
	return rig
}

func (rig *limitRig) free() { rig.once.Do(func() { close(rig.release) }) }

// hold starts n hub requests to path that block in the local API, and waits until they all do.
func (rig *limitRig) hold(path string, n int) {
	rig.t.Helper()
	want := rig.inFlight.Load() + int32(n)
	for i := 0; i < n; i++ {
		go func() {
			req, _ := http.NewRequest("GET", "http://x"+path, nil)
			if resp, err := rig.sess.RoundTrip(req); err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	require.Eventually(rig.t, func() bool { return rig.inFlight.Load() == want }, 5*time.Second, 5*time.Millisecond)
}

func (rig *limitRig) get(path string) (*http.Response, string) {
	rig.t.Helper()
	req, _ := http.NewRequest("GET", "http://x"+path, nil)
	resp, err := rig.sess.RoundTrip(req)
	require.NoError(rig.t, err, path)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func assertBusy(t *testing.T, resp *http.Response, body, what string) {
	t.Helper()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, what)
	assert.Equal(t, "2", resp.Header.Get("Retry-After"), what)
	assert.Equal(t, "connector", resp.Header.Get(LimitHeader), what)
	assert.Contains(t, body, "hub_busy", what)
}

// #3624: N requests fill the global pool; the next gets 429 at once (never queued); a long poll
// and a direct local request still work; a freed slot is usable again.
func TestHubRequestsBeyondTheGlobalPoolGet429(t *testing.T) {
	rig := newLimitRig(t, HubLimits{Global: 3, Long: 2, Search: 1})
	rig.hold("/api/hold/a", 3)

	start := time.Now()
	resp, body := rig.get("/api/hold/b?now=1")
	assertBusy(t, resp, body, "the fourth request")
	assert.Less(t, time.Since(start), 2*time.Second, "refused at once, not queued")

	resp, body = rig.get("/api/board/p/waits/poll?now=1")
	assert.Equal(t, 200, resp.StatusCode, "a long poll has its own pool: %s", body)

	direct, err := http.Get(rig.base + "/api/hold/c?now=1")
	require.NoError(t, err)
	direct.Body.Close()
	assert.Equal(t, 200, direct.StatusCode, "the local dashboard never passes the limiter")

	rig.free()
	require.Eventually(t, func() bool { return rig.inFlight.Load() == 0 }, 5*time.Second, 5*time.Millisecond)
	resp, _ = rig.get("/api/hold/d?now=1")
	assert.Equal(t, 200, resp.StatusCode, "the slots came back")
}

// Held long polls never take the slots normal reads need; a third long poll over the long pool
// is refused, and so is a WebSocket open.
func TestHeldLongPollsDoNotBlockReads(t *testing.T) {
	rig := newLimitRig(t, HubLimits{Global: 2, Long: 2, Search: 1})
	rig.hold("/api/board/p/waits/poll", 2)

	resp, body := rig.get("/api/board/p/peek?now=1")
	assertBusy(t, resp, body, "a third long-lived request")
	_, err := rig.sess.DialWS(context.Background(), "/ws/terminal/x", "", nil)
	var se *StreamError
	require.ErrorAs(t, err, &se, "a WebSocket open over the long pool")
	assert.Equal(t, CodeTooManyStreams, se.Code)

	for i := 0; i < 2; i++ {
		resp, body = rig.get("/api/sessions/live?now=1")
		assert.Equal(t, 200, resp.StatusCode, "a normal read while the long pool is full: %s", body)
	}
}

// The search routes cost more: one search pool slot here, so a second concurrent search is
// refused while ordinary reads still go through.
func TestSearchRoutesHaveACostPool(t *testing.T) {
	rig := newLimitRig(t, HubLimits{Global: 5, Long: 2, Search: 1})
	rig.hold("/api/board/p/messages/all", 1)
	resp, body := rig.get("/api/sessions/history/search?q=x")
	assertBusy(t, resp, body, "a second search")
	resp, _ = rig.get("/api/sessions/live?now=1")
	assert.Equal(t, 200, resp.StatusCode)
}

// A stalled handler can't hold its slot: no response headers within the deadline cancels the
// request, and the slot is free again.
func TestStalledHubRequestIsCancelledAtTheDeadline(t *testing.T) {
	rig := newLimitRig(t, HubLimits{Global: 1, Long: 1, Search: 1, RequestDeadline: 200 * time.Millisecond})
	start := time.Now()
	req, _ := http.NewRequest("GET", "http://x/api/hold/stuck", nil)
	_, err := rig.sess.RoundTrip(req)
	var se *StreamError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, CodeUpstream, se.Code)
	assert.Contains(t, se.Error(), "took too long")
	assert.Less(t, time.Since(start), 3*time.Second)
	require.Eventually(t, func() bool { return rig.inFlight.Load() == 0 }, 5*time.Second, 5*time.Millisecond)
	resp, _ := rig.get("/api/hold/next?now=1")
	assert.Equal(t, 200, resp.StatusCode, "the slot was released")

	// A long poll is not cut at the deadline (its own timeout applies).
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("GET", "http://x/api/board/p/waits/poll", nil)
		resp, err := rig.sess.RoundTrip(req)
		if err != nil {
			done <- 0
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	time.Sleep(500 * time.Millisecond)
	rig.free()
	select {
	case code := <-done:
		assert.Equal(t, 200, code, "a long poll outlives the deadline")
	case <-time.After(5 * time.Second):
		t.Fatal("the long poll never answered")
	}
}

func TestClassify(t *testing.T) {
	for p, want := range map[string]requestClass{
		"/api/board/p/waits/poll":                    classLong,
		"/api/board/p/peek":                          classLong,
		"/api/sessions/history/search":               classSearch,
		"/api/sessions/live/claude-x/history/search": classSearch,
		"/api/sessions/live/claude-x/search-files":   classSearch,
		"/api/board/p/messages/all":                  classSearch,
		"/api/token-usage":                           classSearch,
		"/api/token-usage/by-team":                   classSearch,
		"/api/board/p/messages":                      classGeneral,
		"/api/sessions/live":                         classGeneral,
		"/api/board/p/waits":                         classGeneral,
	} {
		assert.Equal(t, want, classify(KindHTTP, p), p)
	}
	assert.Equal(t, classLong, classify(KindWS, "/ws/terminal/x"))
}
