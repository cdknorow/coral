package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #3438: a chat poll through Coral Hub (no working_directory) reads the session's record
// once, then from memory: with the sessions DB's one connection held by a slow writer, a
// warm poll still answers at once.
func TestChatPollDoesNotWaitForTheSessionsDB(t *testing.T) {
	server, handler, _, ss := setupSessionsTestServer(t)
	defer server.Close()
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	work := "/tmp/chat-meta-work"
	sid := "11111111-2222-3333-4444-555555555555"
	dir := filepath.Join(projects, strings.ReplaceAll(work, "/", "-"))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, sid+".jsonl"),
		[]byte(`{"type":"user","timestamp":"2026-10-14T00:00:00Z","message":{"content":"hello from the hub"}}`+"\n"), 0o644))
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "claude-" + sid, WorkingDir: work}))

	poll := func() (int, []any, time.Duration) {
		c := &http.Client{Timeout: 3 * time.Second}
		start := time.Now()
		resp, err := c.Get(server.URL + "/api/sessions/live/claude-" + sid + "/chat?session_id=" + sid + "&after=0&limit=50")
		require.NoError(t, err)
		defer resp.Body.Close()
		var out struct {
			Messages []any `json:"messages"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out.Messages, time.Since(start)
	}
	code, msgs, _ := poll()
	require.Equal(t, http.StatusOK, code)
	require.Len(t, msgs, 1, "the record gave the working directory")

	// A slow writer holds the sessions DB's only connection.
	tx, err := handler.db.BeginTxx(context.Background(), nil)
	require.NoError(t, err)
	defer tx.Rollback()
	code, msgs, took := poll()
	assert.Equal(t, http.StatusOK, code)
	assert.Len(t, msgs, 1)
	assert.Less(t, took, 500*time.Millisecond, "a warm poll never waits for the DB")
}

func TestChatMetaCacheBounds(t *testing.T) {
	c := newChatMetaCache()
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	c.max = 3
	for i, k := range []string{"a", "b", "c", "d"} {
		c.put(chatMeta{sessionID: k}, k)
		now = now.Add(time.Duration(i+1) * time.Second)
	}
	_, ok := c.get("a")
	assert.False(t, ok, "the oldest is evicted at the cap")
	m, ok := c.get("d")
	require.True(t, ok)
	assert.Equal(t, "d", m.sessionID)
	now = now.Add(2 * time.Minute)
	_, ok = c.get("d")
	assert.False(t, ok, "expired")
	// Forgetting a session drops it under its id and under every name that points at it.
	c.put(chatMeta{sessionID: "x"}, "x", "name-x")
	c.put(chatMeta{sessionID: "y"}, "y", "name-y")
	c.forget("x")
	_, ok = c.get("name-x")
	assert.False(t, ok, "the name pointing at x")
	_, ok = c.get("x")
	assert.False(t, ok)
	_, ok = c.get("name-y")
	assert.True(t, ok, "another session's entry stays")
	c.forget("name-y") // a new session registered under that name
	_, ok = c.get("name-y")
	assert.False(t, ok)
	_, ok = c.get("y")
	assert.True(t, ok)
}

// Killing a session forgets its cached record, under its id and its name.
func TestChatMetaForgottenOnKill(t *testing.T) {
	server, handler, _, ss := setupSessionsTestServer(t)
	defer server.Close()
	sid := "66666666-7777-8888-9999-000000000000"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "claude-" + sid, WorkingDir: "/tmp/w"}))
	handler.chatMeta.put(chatMeta{agentType: "claude", workingDir: "/tmp/w", sessionID: sid}, sid, "claude-"+sid)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-"+sid+"/kill", "application/json",
		strings.NewReader(`{"agent_type":"claude","session_id":"`+sid+`"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	for _, k := range []string{sid, "claude-" + sid} {
		_, ok := handler.chatMeta.get(k)
		assert.False(t, ok, k)
	}
}

// With CORAL_DEBUG_PHASES=1 the metadata_lookup phase says whether the record came from
// memory, so a before/after through the hub shows it directly.
func TestChatMetadataLookupPhaseReportsTheCache(t *testing.T) {
	t.Setenv("CORAL_DEBUG_PHASES", "1")
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)
	server, _, _, ss := setupSessionsTestServer(t)
	defer server.Close()
	sid := "cccccccc-0000-0000-0000-000000000001"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "claude-" + sid, WorkingDir: "/tmp/c"}))
	for i := 0; i < 2; i++ {
		resp, err := http.Get(server.URL + "/api/sessions/live/claude-" + sid + "/chat?session_id=" + sid + "&after=0&limit=10")
		require.NoError(t, err)
		resp.Body.Close()
	}
	var got []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "phase=metadata_lookup") && strings.Contains(line, "request phase complete") {
			i := strings.Index(line, "meta_cache=")
			require.GreaterOrEqual(t, i, 0, line)
			got = append(got, strings.Fields(line[i:])[0])
		}
	}
	assert.Equal(t, []string{"meta_cache=miss", "meta_cache=hit"}, got)
}

// A name shared by two sessions (a folder name) is not cached: each request looks again.
func TestChatMetaSharedNameNotPinned(t *testing.T) {
	server, handler, _, ss := setupSessionsTestServer(t)
	defer server.Close()
	for _, sid := range []string{"aaaaaaaa-0000-0000-0000-000000000001", "aaaaaaaa-0000-0000-0000-000000000002"} {
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "shared-folder", WorkingDir: "/tmp/" + sid}))
	}
	resp, err := http.Get(server.URL + "/api/sessions/live/shared-folder/chat?after=0&limit=10")
	require.NoError(t, err)
	resp.Body.Close()
	_, ok := handler.chatMeta.get("shared-folder")
	assert.False(t, ok, "a shared name is not pinned")

	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: "bbbbbbbb-0000-0000-0000-000000000001", AgentType: "claude", AgentName: "only-one", WorkingDir: "/tmp/b"}))
	resp, err = http.Get(server.URL + "/api/sessions/live/only-one/chat?after=0&limit=10")
	require.NoError(t, err)
	resp.Body.Close()
	m, ok := handler.chatMeta.get("only-one")
	require.True(t, ok, "a name of one session is cached")
	assert.Equal(t, "bbbbbbbb-0000-0000-0000-000000000001", m.sessionID)
}

// The lookup a hub chat poll makes: from memory once warm, against the DB query it replaces.
func BenchmarkChatMetaLookup(b *testing.B) {
	c := newChatMetaCache()
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("%08d-0000-0000-0000-000000000000", i)
		c.put(chatMeta{agentType: "claude", workingDir: "/w", sessionID: id}, id, "claude-"+id)
	}
	key := fmt.Sprintf("claude-%08d-0000-0000-0000-000000000000", 500)
	b.Run("memory", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, ok := c.get(key); !ok {
					b.Fatal("miss")
				}
			}
		})
	})
	b.Run("db", func(b *testing.B) { // what each poll did before
		db, err := storetest.Open(b, filepath.Join(b.TempDir(), "c.db"))
		require.NoError(b, err)
		defer db.Close()
		ss := store.NewSessionStore(db)
		for i := 0; i < 1000; i++ {
			id := fmt.Sprintf("%08d-0000-0000-0000-000000000000", i)
			require.NoError(b, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "claude-" + id, WorkingDir: "/w"}))
		}
		id := fmt.Sprintf("%08d-0000-0000-0000-000000000000", 500)
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if ls, err := ss.GetLiveSession(context.Background(), id); err != nil || ls == nil {
					b.Fatal("miss", err)
				}
			}
		})
	})
}
