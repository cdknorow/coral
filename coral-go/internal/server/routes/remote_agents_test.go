package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/store"
)

// fakeSource is a controllable RemoteAgentSource.
type fakeSource struct {
	mu   sync.Mutex
	snap background.RemoteSnapshot
	subs []chan struct{}
}

func (f *fakeSource) Snapshot() background.RemoteSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := background.RemoteSnapshot{Servers: append([]background.RemoteServerStatus(nil), f.snap.Servers...)}
	for _, m := range f.snap.Sessions {
		c := map[string]any{}
		for k, v := range m {
			c[k] = v
		}
		s.Sessions = append(s.Sessions, c)
	}
	return s
}

func (f *fakeSource) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()
	return ch, func() {}
}

func (f *fakeSource) set(snap background.RemoteSnapshot) {
	f.mu.Lock()
	f.snap = snap
	subs := f.subs
	f.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func seedSleeping(t *testing.T, h *SessionsHandler, name, sid string) {
	t.Helper()
	require.NoError(t, store.NewSessionStore(h.db).RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: name, WorkingDir: "/tmp/x", IsSleeping: 1, CreatedAt: "2026-01-01T00:00:00Z",
	}))
}

func getBody(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, 200, resp.StatusCode, string(b))
	return b
}

func liveServer(t *testing.T, h *SessionsHandler) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(h.List))
	t.Cleanup(srv.Close)
	return srv
}

func TestLiveList_ZeroRemotesUnchanged(t *testing.T) {
	_, h := setupTestServer(t)
	seedSleeping(t, h, "dup", "local-1")
	srv := liveServer(t, h)
	before := getBody(t, srv.URL)

	// An empty source (hub wired, nothing registered) must not change anything.
	h.SetRemoteAgents(&fakeSource{})
	after := getBody(t, srv.URL)
	assert.JSONEq(t, string(before), string(after))
	var arr []map[string]any
	require.NoError(t, json.Unmarshal(after, &arr), "must stay a plain array")
	require.Len(t, arr, 1)
	_, hasServer := arr[0]["server"]
	assert.False(t, hasServer)
}

func TestLiveList_MergeCollisionAndStale(t *testing.T) {
	_, h := setupTestServer(t)
	seedSleeping(t, h, "dup", "local-1")
	src := &fakeSource{}
	h.SetRemoteAgents(src)
	srv := liveServer(t, h)

	src.set(background.RemoteSnapshot{
		Servers: []background.RemoteServerStatus{{ID: "a", Label: "A", Status: "online"}, {ID: "b", Label: "B", Status: "unreachable"}},
		Sessions: []map[string]any{
			{"name": "dup", "session_id": "ra-1", "server": "a"},
			{"name": "dup", "session_id": "rb-1", "server": "b", "stale": true},
		},
	})
	var out struct {
		Sessions []map[string]any                `json:"sessions"`
		Servers  []background.RemoteServerStatus `json:"servers"`
	}
	require.NoError(t, json.Unmarshal(getBody(t, srv.URL), &out))
	require.Len(t, out.Sessions, 3)
	byServer := map[string]map[string]any{}
	for _, s := range out.Sessions {
		assert.Equal(t, "dup", s["name"])
		byServer[s["server"].(string)] = s
	}
	require.Len(t, byServer, 3)
	assert.Nil(t, byServer["local"]["stale"])
	assert.Nil(t, byServer["a"]["stale"])
	assert.Equal(t, true, byServer["b"]["stale"])
	assert.Equal(t, "unreachable", out.Servers[1].Status)
}

func wsRead(t *testing.T, ctx context.Context, c *websocket.Conn) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, wsjson.Read(ctx, c, &m))
	return m
}

func TestWSCoral_RelaysRemoteWithServerTag(t *testing.T) {
	server, h := setupTestServer(t)
	seedSleeping(t, h, "dup", "local-1")
	src := &fakeSource{}
	h.SetRemoteAgents(src)
	src.set(background.RemoteSnapshot{
		Servers:  []background.RemoteServerStatus{{ID: "a", Label: "A", Status: "online"}},
		Sessions: []map[string]any{{"name": "dup", "session_id": "ra-1", "server": "a", "status": "Idle"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[4:]+"/ws/coral", nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	first := wsRead(t, ctx, conn)
	assert.Equal(t, "coral_update", first["type"])
	assert.Len(t, first["servers"], 1)
	sess := first["sessions"].([]any)
	require.Len(t, sess, 2)
	tags := map[string]bool{}
	for _, s := range sess {
		tags[s.(map[string]any)["server"].(string)] = true
	}
	assert.Equal(t, map[string]bool{"local": true, "a": true}, tags)

	// Remote state change arrives as a diff carrying the server tag.
	src.set(background.RemoteSnapshot{
		Servers:  []background.RemoteServerStatus{{ID: "a", Label: "A", Status: "unreachable"}},
		Sessions: []map[string]any{{"name": "dup", "session_id": "ra-1", "server": "a", "status": "Idle", "stale": true}},
	})
	diff := wsRead(t, ctx, conn)
	assert.Equal(t, "coral_diff", diff["type"])
	ch := diff["changed"].([]any)
	require.Len(t, ch, 1)
	assert.Equal(t, "a", ch[0].(map[string]any)["server"])
	assert.Equal(t, true, ch[0].(map[string]any)["stale"])
	assert.Equal(t, "unreachable", diff["servers"].([]any)[0].(map[string]any)["status"])

	// Removal of a remote agent is reported with its server.
	src.set(background.RemoteSnapshot{Servers: []background.RemoteServerStatus{{ID: "a", Label: "A", Status: "unreachable"}}})
	rm := wsRead(t, ctx, conn)
	rr := rm["removed_remote"].([]any)
	require.Len(t, rr, 1)
	assert.Equal(t, map[string]any{"server": "a", "key": "ra-1"}, rr[0])
	assert.Nil(t, rm["removed"])
}

func TestWSCoral_ZeroRemotesNoServerFields(t *testing.T) {
	server, h := setupTestServer(t)
	seedSleeping(t, h, "dup", "local-1")
	h.SetRemoteAgents(&fakeSource{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[4:]+"/ws/coral", nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	first := wsRead(t, ctx, conn)
	assert.NotContains(t, first, "servers")
	assert.NotContains(t, first["sessions"].([]any)[0], "server")
}
