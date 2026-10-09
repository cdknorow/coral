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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/cdknorow/coral/internal/server/proxy"
	"github.com/cdknorow/coral/internal/store"
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
		return proxy.Target{}, proxy.ErrUnknownServer
	}
	return proxy.Target{URL: s.URL, APIKey: testKey, AllowPrivate: true}, nil
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
}

func newFakeRemote(t *testing.T, sessions ...map[string]any) *fakeRemote {
	fr := &fakeRemote{sessions: sessions, feedConn: make(chan *websocket.Conn, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions/live", func(w http.ResponseWriter, r *http.Request) {
		fr.mu.Lock()
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
		if fr.noFeed {
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
		fr.feedConn <- c
		<-r.Context().Done()
	})
	fr.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(fr.Close)
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
		_, stale := s["stale"]
		assert.False(t, stale)
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
		return statusMap(s)["a"] == "online" && len(s.Sessions) == 1 && s.Sessions[0]["name"] == "two" && s.Sessions[0]["stale"] == nil
	}, 5*time.Second, 20*time.Millisecond)
}

func TestHub_UnauthorizedAndRegistryChanges(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	a.noFeed = true
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	// bad: a server that rejects our key
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 403) }))
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
