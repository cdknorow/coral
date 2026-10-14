package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #3461: the live list reads only through the read pools, so a writer holding the
// sessions DB's one write connection (a long transaction, a burst of hook writes)
// doesn't delay it. Before, its token-usage reads (and, before #3441, all of them)
// queued behind the writer and its p95 reached ~1 s under load.
func TestLiveListDoesNotWaitForTheWriter(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	defer server.Close()
	handler.SetTokenStore(store.NewTokenUsageStore(handler.db))
	for _, sid := range []string{"00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"} {
		terminal.addSession("claude-"+sid, "/tmp/w")
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "w", WorkingDir: "/tmp/w", CreatedAt: time.Now().UTC().Format(time.RFC3339)}))
	}
	list := func() (int, int, time.Duration) {
		c := &http.Client{Timeout: 3 * time.Second}
		start := time.Now()
		resp, err := c.Get(server.URL + "/api/sessions/live")
		require.NoError(t, err)
		defer resp.Body.Close()
		var rows []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&rows)
		return resp.StatusCode, len(rows), time.Since(start)
	}
	code, n, _ := list()
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, 2, n)

	tx, err := handler.db.BeginTxx(context.Background(), nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO user_settings(key, value) VALUES ('held-by-a-writer', '1')`)
	require.NoError(t, err)
	code, n, took := list()
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, 2, n)
	assert.Less(t, took, 500*time.Millisecond, "the live list never waits for the writer")
}

func TestRunBoundedLimitsConcurrency(t *testing.T) {
	var running, peak, done atomic.Int32
	var mu sync.Mutex
	fns := make([]func(), 12)
	for i := range fns {
		fns[i] = func() {
			n := running.Add(1)
			mu.Lock()
			if n > peak.Load() {
				peak.Store(n)
			}
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			running.Add(-1)
			done.Add(1)
		}
	}
	runBounded(4, fns)
	assert.EqualValues(t, 12, done.Load(), "all ran, and runBounded waited for them")
	assert.LessOrEqual(t, peak.Load(), int32(4))
	assert.Greater(t, peak.Load(), int32(1), "they ran concurrently")
}
