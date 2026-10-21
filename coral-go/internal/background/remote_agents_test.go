package background

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	proxy "github.com/subgentic/coral/pkg/hubproxy"
)

type fakeRegistry struct {
	mu      sync.Mutex
	servers map[string]store.RemoteServer
	status  map[string]string
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{servers: map[string]store.RemoteServer{}, status: map[string]string{}}
}

func (f *fakeRegistry) put(id, url string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servers[id] = store.RemoteServer{ID: id, Label: strings.ToUpper(id), URL: url, AllowPrivate: true, Status: "unknown"}
}

func (f *fakeRegistry) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.servers, id)
}

func (f *fakeRegistry) List(context.Context) ([]store.RemoteServer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.RemoteServer
	for _, s := range f.servers {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeRegistry) SetStatus(_ context.Context, id, status, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[id] = status
	return nil
}

func (f *fakeRegistry) statusOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status[id]
}

type regResolver struct{ reg *fakeRegistry }

func (r regResolver) Resolve(_ context.Context, id string) (proxy.Target, error) {
	r.reg.mu.Lock()
	defer r.reg.mu.Unlock()
	s, ok := r.reg.servers[id]
	if !ok {
		return proxy.HTTPTarget{}, proxy.ErrUnknownServer
	}
	return proxy.HTTPTarget{URL: s.URL, APIKey: testKey, AllowPrivate: true}, nil
}

const testKey = "k-test"

// fakeRemote serves /api/sessions/live and /ws/coral behind a bearer key.
type fakeRemote struct {
	*httptest.Server
	mu       sync.Mutex
	sessions []map[string]any
	delay    time.Duration
	noFeed   bool
	feedConn chan *websocket.Conn

	healthDelay time.Duration
	noHealthAPI bool // an older remote: /api/health is 404
	feedDeaf    bool // the feed socket is never read, so pings go unanswered
	listGets    int
	healthGets  int
	feedQuery   string        // raw query of the last /ws/coral request
	stop        chan struct{} // closed at cleanup: ends the feeds (a hijacked socket's request context isn't)
}

func (fr *fakeRemote) counts() (list, health int) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.listGets, fr.healthGets
}

func newFakeRemote(t *testing.T, sessions ...map[string]any) *fakeRemote {
	fr := &fakeRemote{sessions: sessions, feedConn: make(chan *websocket.Conn, 8), stop: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		fr.mu.Lock()
		fr.healthGets++
		d, missing := fr.healthDelay, fr.noHealthAPI
		fr.mu.Unlock()
		if missing {
			http.NotFound(w, r)
			return
		}
		if d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": "fake-1"})
	})
	mux.HandleFunc("/api/sessions/live", func(w http.ResponseWriter, r *http.Request) {
		fr.mu.Lock()
		fr.listGets++
		s, d := fr.sessions, fr.delay
		fr.mu.Unlock()
		if d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/ws/coral", func(w http.ResponseWriter, r *http.Request) {
		fr.mu.Lock()
		noFeed := fr.noFeed
		fr.feedQuery = r.URL.RawQuery
		fr.mu.Unlock()
		if noFeed {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		fr.mu.Lock()
		s := fr.sessions
		fr.mu.Unlock()
		wsjson.Write(r.Context(), c, map[string]any{"type": "coral_update", "sessions": s})
		fr.mu.Lock()
		deaf := fr.feedDeaf
		fr.mu.Unlock()
		ctx := r.Context()
		if !deaf {
			ctx = c.CloseRead(ctx) // reading answers the hub's pings, as the real server does
		}
		fr.feedConn <- c
		select {
		case <-ctx.Done():
		case <-fr.stop: // the test is over (#4167: the handler outlived it)
			c.Close(websocket.StatusGoingAway, "test over")
		}
	})
	fr.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { close(fr.stop); fr.Close() })
	return fr
}

func (fr *fakeRemote) set(s ...map[string]any) {
	fr.mu.Lock()
	fr.sessions = s
	fr.mu.Unlock()
}

func sess(name, sid string) map[string]any {
	return map[string]any{"name": name, "session_id": sid, "status": "Working"}
}

func fastHub(reg *fakeRegistry) *RemoteAgentHub {
	return NewRemoteAgentHub(reg, regResolver{reg}, RemoteAgentHubConfig{
		PollInterval: 80 * time.Millisecond, RequestTimeout: 300 * time.Millisecond,
		RegistryRefresh: 50 * time.Millisecond, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		OfflineGrace: -1, // these tests check each failure's status; the grace has its own (#3649)
	})
}

func startHub(t *testing.T, h *RemoteAgentHub) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func statusMap(s RemoteSnapshot) map[string]string {
	m := map[string]string{}
	for _, x := range s.Servers {
		m[x.ID] = x.Status
	}
	return m
}

func TestHub_MergeCollisionAndStatus(t *testing.T) {
	a := newFakeRemote(t, sess("worker", "a-1"))
	b := newFakeRemote(t, sess("worker", "b-1"))
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	reg.put("b", b.URL)
	a.noFeed, b.noFeed = true, true // poll path only
	h := fastHub(reg)
	startHub(t, h)

	require.Eventually(t, func() bool { return len(h.Snapshot().Sessions) == 2 }, 3*time.Second, 20*time.Millisecond)
	snap := h.Snapshot()
	assert.Equal(t, map[string]string{"a": "online", "b": "online"}, statusMap(snap))
	servers := map[string]bool{}
	for _, s := range snap.Sessions {
		assert.Equal(t, "worker", s["name"])
		assert.Equal(t, false, s["stale"], "an online remote's sessions carry stale:false explicitly (#3159)")
		servers[s["server"].(string)] = true
	}
	assert.Equal(t, map[string]bool{"a": true, "b": true}, servers)
	assert.Equal(t, "A", snap.Servers[0].Label)
	require.Eventually(t, func() bool { return reg.statusOf("a") == "online" }, time.Second, 10*time.Millisecond)
}

func TestHub_OfflineStaleThenRecovery(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	a.noFeed = true
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := fastHub(reg)
	startHub(t, h)
	require.Eventually(t, func() bool { return len(h.Snapshot().Sessions) == 1 }, 3*time.Second, 20*time.Millisecond)

	addr := a.Listener.Addr().String()
	a.Close()
	require.Eventually(t, func() bool {
		s := h.Snapshot()
		return statusMap(s)["a"] == "unreachable" && len(s.Sessions) == 1 && s.Sessions[0]["stale"] == true
	}, 3*time.Second, 20*time.Millisecond)
	assert.Equal(t, "unreachable", reg.statusOf("a"))

	// Bring a server back on the same address.
	ln, err := newListener(addr)
	require.NoError(t, err)
	back := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			http.Error(w, "no", 401)
			return
		}
		if r.URL.Path == "/api/sessions/live" {
			json.NewEncoder(w).Encode([]map[string]any{sess("two", "a-2")})
			return
		}
		http.NotFound(w, r)
	}))
	back.Listener.Close()
	back.Listener = ln
	back.Start()
	defer back.Close()

	require.Eventually(t, func() bool {
		s := h.Snapshot()
		return statusMap(s)["a"] == "online" && len(s.Sessions) == 1 && s.Sessions[0]["name"] == "two" && s.Sessions[0]["stale"] == false
	}, 5*time.Second, 20*time.Millisecond)
}

func TestHub_UnauthorizedAndRegistryChanges(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	a.noFeed = true
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	// bad: a server that rejects our key, as Coral's auth layer does (a plain 401; #3649: a bare
	// 403 is an answer about the request, not about the key)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "Unauthorized", 401) }))
	defer bad.Close()
	reg.put("bad", bad.URL)
	h := fastHub(reg)
	startHub(t, h)

	require.Eventually(t, func() bool {
		m := statusMap(h.Snapshot())
		return m["a"] == "online" && m["bad"] == "unauthorized"
	}, 3*time.Second, 20*time.Millisecond)

	// Live removal / addition without restart.
	reg.remove("a")
	require.Eventually(t, func() bool {
		s := h.Snapshot()
		_, ok := statusMap(s)["a"]
		return !ok && len(s.Sessions) == 0
	}, 3*time.Second, 20*time.Millisecond)

	c := newFakeRemote(t, sess("c1", "c-1"))
	c.noFeed = true
	reg.put("c", c.URL)
	require.Eventually(t, func() bool { return len(h.Snapshot().Sessions) == 1 }, 3*time.Second, 20*time.Millisecond)
}

func TestHub_SlowRemoteDoesNotBlockOthers(t *testing.T) {
	slow := newFakeRemote(t, sess("slow", "s-1"))
	slow.delay = 5 * time.Second
	slow.noFeed = true
	fast := newFakeRemote(t, sess("fast", "f-1"))
	fast.noFeed = true
	reg := newFakeRegistry()
	reg.put("slow", slow.URL)
	reg.put("fast", fast.URL)
	h := fastHub(reg)
	startHub(t, h)

	require.Eventually(t, func() bool { return statusMap(h.Snapshot())["fast"] == "online" }, time.Second, 10*time.Millisecond)
	fast.set(sess("fast", "f-1"), sess("fast2", "f-2"))
	require.Eventually(t, func() bool { return len(h.Snapshot().Sessions) == 2 }, time.Second, 10*time.Millisecond)
	assert.NotEqual(t, "online", statusMap(h.Snapshot())["slow"])
}

func TestHub_FeedRelayAndFallback(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := fastHub(reg)
	startHub(t, h)
	sub, cancel := h.Subscribe()
	defer cancel()

	var conn *websocket.Conn
	select {
	case conn = <-a.feedConn:
	case <-time.After(3 * time.Second):
		t.Fatal("hub never opened an upstream feed socket")
	}
	// Push a diff over the real socket; poll data stays at the old value, so
	// seeing the change proves the feed is what updated the cache.
	require.NoError(t, wsjson.Write(context.Background(), conn, map[string]any{
		"type": "coral_diff", "changed": []map[string]any{{"name": "one", "session_id": "a-1", "status": "Idle"}, sess("two", "a-2")},
	}))
	require.Eventually(t, func() bool {
		for _, s := range h.Snapshot().Sessions {
			if s["session_id"] == "a-1" && s["status"] == "Idle" {
				return len(h.Snapshot().Sessions) == 2
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond)
	select {
	case <-sub:
	default:
		t.Fatal("subscriber not notified")
	}

	require.NoError(t, wsjson.Write(context.Background(), conn, map[string]any{"type": "coral_diff", "removed": []string{"a-2"}}))
	require.Eventually(t, func() bool { return len(h.Snapshot().Sessions) == 1 }, 3*time.Second, 20*time.Millisecond)

	// Drop the feed: the poller's data takes over again.
	conn.Close(websocket.StatusNormalClosure, "")
	a.set(sess("polled", "p-1"))
	require.Eventually(t, func() bool {
		s := h.Snapshot().Sessions
		return len(s) == 1 && s[0]["name"] == "polled"
	}, 5*time.Second, 20*time.Millisecond)
}

func newListener(addr string) (net.Listener, error) {
	var err error
	for i := 0; i < 50; i++ {
		var ln net.Listener
		if ln, err = net.Listen("tcp", addr); err == nil {
			return ln, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, err
}

// feedHub is fastHub with a feed quiet window long enough not to trigger.
func feedHub(reg *fakeRegistry, quiet time.Duration) *RemoteAgentHub {
	return NewRemoteAgentHub(reg, regResolver{reg}, RemoteAgentHubConfig{
		PollInterval: 80 * time.Millisecond, RequestTimeout: 300 * time.Millisecond,
		RegistryRefresh: 50 * time.Millisecond, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		FeedQuietRefresh: quiet,
		FeedPingInterval: 80 * time.Millisecond,
		OfflineGrace:     -1, // each failure's status at once (#3649 tests the grace on its own)
	})
}

// awaitHealthCheck waits for one more health check: a list poll already under way when the feed
// went live has then ended, and won't be counted as one made with the feed live (#3788).
func awaitHealthCheck(t *testing.T, a *fakeRemote) {
	t.Helper()
	_, h0 := a.counts()
	require.Eventually(t, func() bool { _, h := a.counts(); return h > h0 }, 3*time.Second, 5*time.Millisecond)
}

// While the feed is live and active, the hub does not re-fetch the list: it
// only checks /api/health, and keeps the remote online.
func TestHub_FeedLiveSkipsListPoll(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := feedHub(reg, time.Hour)
	startHub(t, h)
	var conn *websocket.Conn
	select {
	case conn = <-a.feedConn:
	case <-time.After(3 * time.Second):
		t.Fatal("hub never opened an upstream feed socket")
	}
	require.Eventually(t, func() bool { return h.feedCarriesList("a") }, 2*time.Second, 10*time.Millisecond)
	awaitHealthCheck(t, a)
	list0, health0 := a.counts()
	time.Sleep(800 * time.Millisecond) // ~10 poll intervals
	list1, health1 := a.counts()
	assert.Equal(t, list0, list1, "list must not be polled while the feed is live")
	assert.GreaterOrEqual(t, health1-health0, 5, "liveness is checked every poll interval")
	assert.Equal(t, "online", statusMap(h.Snapshot())["a"])
	assert.Equal(t, "fake-1", h.Snapshot().Servers[0].Version, "version comes from the health check")

	// Feed data still flows into the list.
	require.NoError(t, wsjson.Write(context.Background(), conn, map[string]any{
		"type": "coral_diff", "changed": []map[string]any{{"name": "one", "session_id": "a-1", "status": "Idle"}},
	}))
	require.Eventually(t, func() bool {
		s := h.Snapshot().Sessions
		return len(s) == 1 && s[0]["status"] == "Idle"
	}, 2*time.Second, 10*time.Millisecond)

	// When the feed drops (and cannot reconnect), list polling resumes.
	a.mu.Lock()
	a.noFeed = true
	a.mu.Unlock()
	conn.Close(websocket.StatusNormalClosure, "")
	require.Eventually(t, func() bool { l, _ := a.counts(); return l > list1 }, 3*time.Second, 10*time.Millisecond)
}

// A feed that has been quiet for FeedQuietRefresh no longer suppresses the list
// poll: a remote whose handlers hang can keep a silent socket open.
func TestHub_QuietFeedRefreshesList(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := feedHub(reg, 400*time.Millisecond)
	startHub(t, h)
	select {
	case <-a.feedConn:
	case <-time.After(3 * time.Second):
		t.Fatal("hub never opened an upstream feed socket")
	}
	require.Eventually(t, func() bool { return h.feedCarriesList("a") }, 2*time.Second, 10*time.Millisecond)
	list0, _ := a.counts()
	a.set(sess("polled", "p-1")) // only visible through the list
	require.Eventually(t, func() bool {
		l, _ := a.counts()
		s := h.Snapshot().Sessions
		return l > list0 && !h.feedCarriesList("a") && len(s) == 1
	}, 3*time.Second, 10*time.Millisecond)
}

// A remote whose HTTP hangs while its feed socket stays open (and answers
// pings) is still marked non-online within about RequestTimeout+PollInterval.
func TestHub_HungRemoteWithLiveFeedGoesStale(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := feedHub(reg, time.Hour)
	startHub(t, h)
	select {
	case <-a.feedConn:
	case <-time.After(3 * time.Second):
		t.Fatal("hub never opened an upstream feed socket")
	}
	require.Eventually(t, func() bool { return h.feedCarriesList("a") }, 2*time.Second, 10*time.Millisecond)
	a.mu.Lock()
	a.healthDelay, a.delay = 5*time.Second, 5*time.Second
	a.mu.Unlock()
	start := time.Now()
	require.Eventually(t, func() bool { return statusMap(h.Snapshot())["a"] != "online" }, 2*time.Second, 10*time.Millisecond)
	assert.Less(t, time.Since(start), 300*time.Millisecond+80*time.Millisecond+200*time.Millisecond, "detected within ~timeout + one poll interval")
	for _, s := range h.Snapshot().Sessions {
		assert.Equal(t, true, s["stale"])
	}
}

// A feed socket whose pings go unanswered is dropped, so list polling resumes.
func TestHub_UnansweredPingDropsFeed(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	a.feedDeaf = true
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := feedHub(reg, time.Hour)
	startHub(t, h)
	select {
	case <-a.feedConn:
	case <-time.After(3 * time.Second):
		t.Fatal("hub never opened an upstream feed socket")
	}
	require.Eventually(t, func() bool { return h.feedCarriesList("a") }, 2*time.Second, 10*time.Millisecond)
	// Ping every 80ms, 300ms timeout: the feed must drop and the list resume.
	require.Eventually(t, func() bool { return !h.feedCarriesList("a") }, 2*time.Second, 10*time.Millisecond)
	list0, _ := a.counts()
	require.Eventually(t, func() bool { l, _ := a.counts(); return l > list0 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "online", statusMap(h.Snapshot())["a"], "a dropped feed alone does not make the remote unreachable")
}

// An idle remote's feed sends only coral_heartbeat (no diffs). Heartbeats keep
// the feed fresh, so the list is not polled again after the quiet window.
func TestHub_HeartbeatOnlyFeedStaysFresh(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := feedHub(reg, 300*time.Millisecond)
	startHub(t, h)
	var conn *websocket.Conn
	select {
	case conn = <-a.feedConn:
	case <-time.After(3 * time.Second):
		t.Fatal("hub never opened an upstream feed socket")
	}
	require.Eventually(t, func() bool { return h.feedCarriesList("a") }, 2*time.Second, 10*time.Millisecond)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				_ = wsjson.Write(context.Background(), conn, map[string]any{"type": "coral_heartbeat"})
			}
		}
	}()
	awaitHealthCheck(t, a)
	list0, _ := a.counts()
	time.Sleep(time.Second) // > 3x the quiet window
	list1, health1 := a.counts()
	assert.Equal(t, list0, list1, "heartbeats keep the feed fresh: no list polls")
	assert.Greater(t, health1, 0)
	assert.True(t, h.feedCarriesList("a"))
}

// A remote without /api/health (older version) whose feed is live stays online:
// the 404 switches it to list polling instead of marking it unreachable.
func TestHub_NoHealthEndpointFallsBackToList(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	a.noHealthAPI = true
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := feedHub(reg, time.Hour)
	startHub(t, h)
	select {
	case <-a.feedConn:
	case <-time.After(3 * time.Second):
		t.Fatal("hub never opened an upstream feed socket")
	}
	list0, _ := a.counts()
	time.Sleep(800 * time.Millisecond)
	list1, health1 := a.counts()
	assert.Greater(t, list1, list0, "list polling continues")
	assert.LessOrEqual(t, health1, 3, "health is not retried every poll once it 404s")
	assert.Equal(t, "online", statusMap(h.Snapshot())["a"])
	assert.False(t, h.feedCarriesList("a"))
}

// The hub's upstream feed identifies itself as a machine feed, so the remote
// keeps its UI notifications for its own dashboards.
func TestHub_FeedDialsAsHubFeed(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := feedHub(reg, time.Hour)
	startHub(t, h)
	select {
	case <-a.feedConn:
	case <-time.After(3 * time.Second):
		t.Fatal("hub never opened an upstream feed socket")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, "feed=hub", a.feedQuery)
}

// A remote's agent count leaves out its archived agents (#3030).
func TestHub_AgentCountExcludesArchived(t *testing.T) {
	archived := sess("old", "a-2")
	archived["sleeping"], archived["archived"] = true, true
	a := newFakeRemote(t, sess("worker", "a-1"), archived)
	a.noFeed = true
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := fastHub(reg)
	startHub(t, h)
	require.Eventually(t, func() bool { return len(h.Snapshot().Sessions) == 2 }, 3*time.Second, 20*time.Millisecond)
	assert.Equal(t, 1, h.Snapshot().Servers[0].Agents, "the archived agent is still listed but not counted")
}
