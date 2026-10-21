package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/store"
)

func TestAgentWriteBucket(t *testing.T) {
	now := time.Now()
	l := newAgentWriteLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < agentWriteBurst; i++ {
		ok, _ := l.take("a")
		require.True(t, ok, "write %d of the burst", i)
	}
	ok, wait := l.take("a")
	assert.False(t, ok, "the 61st at once is refused")
	assert.InDelta(t, 250*time.Millisecond, wait, float64(30*time.Millisecond), "one token every 250 ms")
	other, _ := l.take("b")
	assert.True(t, other, "another session has its own bucket")
	now = now.Add(time.Second) // four tokens
	for i := 0; i < 4; i++ {
		ok, _ = l.take("a")
		assert.True(t, ok, "refilled write %d", i)
	}
	ok, _ = l.take("a")
	assert.False(t, ok)
	now = now.Add(time.Hour) // refills to the burst, never past it
	granted := 0
	for i := 0; i < 200; i++ {
		if ok, _ := l.take("a"); ok {
			granted++
		}
	}
	assert.Equal(t, agentWriteBurst, granted)
}

func TestAgentWriteBucketsAreBounded(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	now := time.Now()
	l := newAgentWriteLimiter()
	l.now = func() time.Time { return now }
	// Past the cap by 2,000 keys: each insert at the cap prunes the whole map, so three times the cap took seconds for nothing more (#4852).
	for i := 0; i < agentWriteMaxKeys+2000; i++ {
		l.take(fmt.Sprintf("session-%d", i))
		now = now.Add(time.Millisecond)
	}
	assert.LessOrEqual(t, len(l.buckets), agentWriteMaxKeys)
	// Idle buckets go: after the idle time the next insert past 1,000 prunes them.
	now = now.Add(agentWriteMaxIdle + time.Minute)
	l.take("fresh")
	assert.Less(t, len(l.buckets), 1000, "idle buckets are dropped")
}

// The middleware counts only an agent's token on the routes an agent can loop on.
func TestAgentWriteLimitMiddleware(t *testing.T) {
	served := 0
	h := AgentWriteLimit()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++; w.WriteHeader(http.StatusNoContent) }))
	do := func(method, path string, agent bool) int {
		req := httptest.NewRequest(method, path, nil)
		if agent {
			req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: "11111111-1111-4111-8111-111111111111"}))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			assert.NotEmpty(t, rec.Header().Get("Retry-After"))
			assert.Contains(t, rec.Body.String(), `"code":"rate_limited"`)
			assert.Equal(t, "action", rec.Header().Get(httputil.ActionLimitHeader), "marked as a limit on this sender, not an overloaded server")
			var reply map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reply))
			assert.NotEmpty(t, reply["message"], "coral-board prints the message before the code")
			assert.Equal(t, reply["error"], reply["message"])
		}
		return rec.Code
	}
	for i := 0; i < agentWriteBurst; i++ {
		require.Equal(t, http.StatusNoContent, do(http.MethodPost, "/api/board/proj/messages", true))
	}
	assert.Equal(t, http.StatusTooManyRequests, do(http.MethodPost, "/api/board/proj/messages", true), "a board post past the burst")
	// The one budget is shared by the limited routes: a task, a notification, a panel are out of tokens too.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/board/proj/tasks"}, {http.MethodPost, "/api/agent/tasks"}, {http.MethodPost, "/api/board/proj/waits"},
		{http.MethodPost, "/api/notifications"}, {http.MethodPut, "/api/agent/ui/panel1"}, {http.MethodPost, "/api/agent/ui/panel1/events"}, {http.MethodPost, "/api/agent/ui-request"},
	} {
		assert.Equal(t, http.StatusTooManyRequests, do(c.method, c.path, true), c.method+" "+c.path)
	}
	// Not counted: reads, the hooks' events, an artifact upload, other writes, and anyone who is not an agent token.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/board/proj/messages"}, {http.MethodPost, "/api/sessions/live/x/events"}, {http.MethodPost, "/api/agent/artifacts"},
		{http.MethodPost, "/api/board/proj/messages/mark-read"}, {http.MethodPost, "/api/board/proj/subscribe"}, {http.MethodDelete, "/api/board/proj/messages"},
	} {
		assert.Equal(t, http.StatusNoContent, do(c.method, c.path, true), c.method+" "+c.path+" by the agent")
	}
	for i := 0; i < 3*agentWriteBurst; i++ {
		require.Equal(t, http.StatusNoContent, do(http.MethodPost, "/api/board/proj/messages", false), "the Operator or the key is never limited")
	}
	assert.Greater(t, served, 3*agentWriteBurst)
}

// #4964: chi routes on the raw path, so a board name with an encoded slash reaches the messages handler while its decoded path has an extra
// segment. Those requests draw from the same budget (they were once counted by neither spelling's decoded form).
func TestAgentWriteLimitCountsAnEncodedSlashInTheBoardName(t *testing.T) {
	h := AgentWriteLimit()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	do := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: "22222222-2222-4222-8222-222222222222"}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < agentWriteBurst; i++ {
		require.Equal(t, http.StatusNoContent, do("/api/board/a%2Fb/messages"), "inside the burst")
	}
	assert.Equal(t, http.StatusTooManyRequests, do("/api/board/a%2fb/messages"), "an encoded slash (either case) is counted like any board post")
	assert.Equal(t, http.StatusTooManyRequests, do("/api/board/plain/messages"), "and it was the same budget")
	assert.True(t, limitedAgentWrite(httptest.NewRequest(http.MethodPost, "/api/board/a%2Fb/tasks", nil)))
}
