package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/tunnel"
)

// #5841: Idempotency-Key for the routes that start something, through Coral Hub: once per (route, hub user, key); never across users.

const idemKey = "0123456789abcdef-launch-1"

func idemReq(path, body, user, key string) *http.Request {
	req := stamp(httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	if user != "" {
		req.Header.Set(tunnel.HubUserHeader, user)
	}
	if key != "" {
		req.Header.Set(tunnel.IdempotencyKeyHeader, key)
	}
	return req
}

// counting is a handler that starts "something": it counts its runs and answers with the count and the status the test chose.
type counting struct {
	runs   atomic.Int32
	status atomic.Int32
}

func newCounting(status int) *counting {
	c := &counting{}
	c.status.Store(int32(status))
	return c
}

func (c *counting) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := c.runs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(c.status.Load()))
		_ = json.NewEncoder(w).Encode(map[string]any{"run": n})
	}
}

func idemDo(h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestIdempotentRequestRunsOnceAndIsAnsweredAgain(t *testing.T) {
	c, i := newCounting(200), NewIdempotency()
	h := i.Wrap(c.handler())
	first := idemDo(h, idemReq("/api/sessions/launch", `{"a":1}`, "hub-user-1234abcd", idemKey))
	second := idemDo(h, idemReq("/api/sessions/launch", `{"a":1}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, 200, first.Code)
	assert.Empty(t, first.Header().Get("Idempotent-Replay"), "the first answer is the handler's own")
	assert.Equal(t, 200, second.Code)
	assert.Equal(t, "true", second.Header().Get("Idempotent-Replay"))
	assert.Equal(t, first.Body.String(), second.Body.String(), "the same answer")
	assert.Equal(t, "application/json", second.Header().Get("Content-Type"))
	assert.EqualValues(t, 1, c.runs.Load(), "it started once")
}

func TestIdempotencyKeyIsPerHubUserAndPerRoute(t *testing.T) {
	c, i := newCounting(200), NewIdempotency()
	h := i.Wrap(c.handler())
	idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey))
	other := idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-9999ffff", idemKey))
	assert.Empty(t, other.Header().Get("Idempotent-Replay"), "another hub user is never given someone else's answer")
	assert.EqualValues(t, 2, c.runs.Load())
	route := idemDo(h, idemReq("/api/sessions/launch-team", `{}`, "hub-user-1234abcd", idemKey))
	assert.Empty(t, route.Header().Get("Idempotent-Replay"), "the same key on another route is another request")
	assert.EqualValues(t, 3, c.runs.Load())
	again := idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-9999ffff", idemKey))
	assert.Equal(t, "true", again.Header().Get("Idempotent-Replay"), "each user's own repeat is answered again")
	assert.EqualValues(t, 3, c.runs.Load())
}

// A Hub request with a key and no valid hub-user label is not deduplicated: it runs fresh and the key is ignored (never one shared "hub" caller).
func TestHubRequestWithAKeyButNoHubUserIsNotDeduplicated(t *testing.T) {
	for name, mutate := range map[string]func(*http.Request){
		"no label":        func(r *http.Request) { r.Header.Del(tunnel.HubUserHeader) },
		"a malformed one": func(r *http.Request) { r.Header.Set(tunnel.HubUserHeader, "someone") },
		"too long":        func(r *http.Request) { r.Header.Set(tunnel.HubUserHeader, "hub-user-1234abcd"+strings.Repeat("0", 40)) },
		"two labels": func(r *http.Request) {
			r.Header.Set(tunnel.HubUserHeader, "hub-user-1234abcd")
			r.Header.Add(tunnel.HubUserHeader, "hub-user-9999ffff")
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, i := newCounting(200), NewIdempotency()
			h := i.Wrap(c.handler())
			for n := 1; n <= 3; n++ {
				req := idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey)
				mutate(req)
				rec := idemDo(h, req)
				assert.Equal(t, 200, rec.Code)
				assert.Empty(t, rec.Header().Get("Idempotent-Replay"))
			}
			assert.EqualValues(t, 3, c.runs.Load(), "every request runs: the key is ignored")
			assert.Empty(t, i.entries, "nothing was recorded")
		})
	}
}

// Only a request through Coral Hub is touched: the dashboard, the command line, an agent, and a forged stamp are not deduplicated, with the header or without.
func TestOnlyHubRequestsAreDeduplicated(t *testing.T) {
	c, i := newCounting(200), NewIdempotency()
	h := i.Wrap(c.handler())
	local := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch", strings.NewReader(`{}`))
		req.Header.Set(tunnel.HubUserHeader, "hub-user-1234abcd")
		req.Header.Set(tunnel.IdempotencyKeyHeader, idemKey)
		return req
	}
	forged := func() *http.Request {
		req := local()
		req.Header.Set(tunnel.ViaHeader, "forged")
		return req
	}
	for _, mk := range []func() *http.Request{local, forged} {
		idemDo(h, mk())
		idemDo(h, mk())
	}
	assert.EqualValues(t, 4, c.runs.Load(), "a local request and a forged stamp run every time")
	assert.Empty(t, i.entries)
	// No key at all: unchanged.
	idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", ""))
	idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", ""))
	assert.EqualValues(t, 6, c.runs.Load())
}

func TestIdempotencyKeyWithAnotherBodyIsRefused(t *testing.T) {
	c, i := newCounting(200), NewIdempotency()
	h := i.Wrap(c.handler())
	idemDo(h, idemReq("/api/sessions/launch", `{"a":1}`, "hub-user-1234abcd", idemKey))
	rec := idemDo(h, idemReq("/api/sessions/launch", `{"a":2}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "idempotency_key_reuse")
	assert.EqualValues(t, 1, c.runs.Load(), "the other request did not run")
	same := idemDo(h, idemReq("/api/sessions/launch", `{"a":1}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, "true", same.Header().Get("Idempotent-Replay"), "the first request is still answered")
}

func TestARepeatWhileTheFirstRunsIsAnsweredInProgress(t *testing.T) {
	i := NewIdempotency()
	release, started := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	h := i.Wrap(func(w http.ResponseWriter, r *http.Request) {
		if runs.Add(1) == 1 {
			close(started)
			<-release
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	var wg sync.WaitGroup
	wg.Add(1)
	var first *httptest.ResponseRecorder
	go func() {
		defer wg.Done()
		first = idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey))
	}()
	<-started
	rec := idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "request_in_progress")
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	close(release)
	wg.Wait()
	assert.Equal(t, 200, first.Code)
	after := idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, "true", after.Header().Get("Idempotent-Replay"))
	assert.EqualValues(t, 1, runs.Load(), "started once")
}

// Only a success is kept: a refusal or a failure started nothing, so the same request is run again (the owner may have turned a switch on meanwhile).
func TestOnlyASuccessIsKept(t *testing.T) {
	for _, status := range []int{400, 403, 409, 422, 500, 503} {
		c, i := newCounting(status), NewIdempotency()
		h := i.Wrap(c.handler())
		idemDo(h, idemReq("/api/sessions/launch-team", `{}`, "hub-user-1234abcd", idemKey))
		c.status.Store(200)
		rec := idemDo(h, idemReq("/api/sessions/launch-team", `{}`, "hub-user-1234abcd", idemKey))
		assert.Equal(t, 200, rec.Code, "%d: the retry ran", status)
		assert.Empty(t, rec.Header().Get("Idempotent-Replay"), "%d", status)
		assert.EqualValues(t, 2, c.runs.Load(), "%d", status)
	}
}

func TestMalformedIdempotencyKeyIsRefused(t *testing.T) {
	c, i := newCounting(200), NewIdempotency()
	h := i.Wrap(c.handler())
	for name, key := range map[string]string{"short": "short", "spaces": "0123456789abcdef key", "bad characters": "0123456789abcdef;drop", "too long": strings.Repeat("a", 129)} {
		rec := idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", key))
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.Contains(t, rec.Body.String(), "invalid_idempotency_key", name)
	}
	two := idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey)
	two.Header.Add(tunnel.IdempotencyKeyHeader, idemKey+"-2")
	assert.Equal(t, http.StatusBadRequest, idemDo(h, two).Code, "two keys")
	assert.EqualValues(t, 0, c.runs.Load(), "none of them ran")
}

func TestIdempotencyRecordsExpireAndAreBounded(t *testing.T) {
	c, i := newCounting(200), NewIdempotency()
	clock := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	i.now = func() time.Time { return clock }
	h := i.Wrap(c.handler())
	idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey))
	clock = clock.Add(idempotencyTTL - time.Second)
	assert.Equal(t, "true", idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey)).Header().Get("Idempotent-Replay"), "within the time to live")
	clock = clock.Add(2 * time.Second)
	assert.Empty(t, idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey)).Header().Get("Idempotent-Replay"), "after it: run again")
	assert.EqualValues(t, 2, c.runs.Load())

	// One user's keys are capped; the oldest finished one goes.
	for n := 0; n < idempotencyMaxPerUser+30; n++ {
		clock = clock.Add(time.Millisecond)
		idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-5555aaaa", "0123456789abcdef-n"+strings.Repeat("x", 3)+string(rune('a'+n%26))+strings.Repeat("0", n/26)))
	}
	assert.LessOrEqual(t, i.perUser["hub-user-5555aaaa"], idempotencyMaxPerUser)
	assert.LessOrEqual(t, len(i.entries), idempotencyMaxKeys)
}

func TestAPanickingHandlerKeepsNothing(t *testing.T) {
	i := NewIdempotency()
	var runs atomic.Int32
	h := i.Wrap(func(w http.ResponseWriter, r *http.Request) {
		if runs.Add(1) == 1 {
			panic("boom")
		}
		w.WriteHeader(200)
	})
	require.Panics(t, func() { idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey)) })
	rec := idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, 200, rec.Code, "the retry ran: the panicked request was not left in progress")
	assert.Empty(t, rec.Header().Get("Idempotent-Replay"))
	assert.EqualValues(t, 2, runs.Load())
}

// The middleware form keys on tunnel.ForwardsIdempotencyKey: the five routes are deduplicated, every other route (a team's wake included) runs every time, key or not.
func TestIdempotencyMiddlewareCoversExactlyTheFiveRoutes(t *testing.T) {
	c, i := newCounting(200), NewIdempotency()
	h := i.Middleware(c.handler())
	serve := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, idemReq(path, `{}`, "hub-user-1234abcd", idemKey))
		return rec
	}
	for _, path := range []string{"/api/sessions/launch", "/api/sessions/launch-team", "/api/sessions/live/claude-x/resume", "/api/sessions/live/claude-x/wake", "/api/tasks/run"} {
		before := c.runs.Load()
		serve(path)
		again := serve(path)
		assert.Equal(t, "true", again.Header().Get("Idempotent-Replay"), path)
		assert.EqualValues(t, before+1, c.runs.Load(), "%s ran once", path)
	}
	for _, path := range []string{"/api/sessions/live/team/b-1/wake", "/api/sessions/live/wake-all", "/api/sessions/live/claude-x/restart", "/api/board/team/resume", "/api/sessions/launch/"} {
		before := c.runs.Load()
		serve(path)
		again := serve(path)
		assert.Empty(t, again.Header().Get("Idempotent-Replay"), path)
		assert.EqualValues(t, before+2, c.runs.Load(), "%s ran every time", path)
	}
}
