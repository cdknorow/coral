package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// #5855 (Security's LOW 1): the query string is part of the request: the same key and body with another query is a reuse, not a replay.
func TestIdempotencyKeyWithAnotherQueryIsRefused(t *testing.T) {
	c, i := newCounting(200), NewIdempotency()
	h := i.Wrap(c.handler())
	withQuery := func(q string) *http.Request {
		req := idemReq("/api/tasks/run", `{"a":1}`, "hub-user-1234abcd", idemKey)
		req.URL.RawQuery = q
		return req
	}
	idemDo(h, withQuery("x=1"))
	rec := idemDo(h, withQuery("x=2"))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "idempotency_key_reuse")
	assert.Equal(t, "true", idemDo(h, withQuery("x=1")).Header().Get("Idempotent-Replay"), "the same query is answered again")
	assert.EqualValues(t, 1, c.runs.Load())
}

// bigAnswer is a handler whose answer is n bytes, counting its runs.
func bigAnswer(n int, runs *atomic.Int32) http.HandlerFunc {
	body := []byte(strings.Repeat("x", n))
	return func(w http.ResponseWriter, r *http.Request) {
		runs.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}
}

// #5855 (LOW 2): the answers kept have a byte budget: past it the oldest finished ones go (a repeat of those runs again), the newest is still answered again.
func TestIdempotencyAnswersAreKeptWithinAByteBudget(t *testing.T) {
	var runs atomic.Int32
	i := NewIdempotency()
	h := i.Wrap(bigAnswer(600<<10, &runs))
	clock := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	i.now = func() time.Time { return clock }
	key := func(n int) string {
		return "0123456789abcdef-budget-" + strings.Repeat("k", n/10) + string(rune('a'+n%10))
	}
	const requests = 60 // 60 x 600 KiB is more than the 32 MiB budget
	user := func(n int) string {
		return []string{"hub-user-1111aaaa", "hub-user-2222bbbb", "hub-user-3333cccc"}[n%3]
	} // three users: the per-user cap is not what bounds it
	for n := 0; n < requests; n++ {
		clock = clock.Add(time.Second)
		idemDo(h, idemReq("/api/sessions/launch-team", `{}`, user(n), key(n)))
	}
	assert.LessOrEqual(t, i.bytes, idempotencyMaxBytes, "the kept answers fit the budget")
	assert.Less(t, len(i.entries), requests, "some of the oldest were dropped")
	before := runs.Load()
	last := idemDo(h, idemReq("/api/sessions/launch-team", `{}`, user(requests-1), key(requests-1)))
	assert.Equal(t, "true", last.Header().Get("Idempotent-Replay"), "the newest is still answered again")
	assert.Equal(t, before, runs.Load())
	idemDo(h, idemReq("/api/sessions/launch-team", `{}`, user(0), key(0)))
	assert.Equal(t, before+1, runs.Load(), "the oldest was evicted: it runs again")
}

// An answer too big to keep is not run again either: a repeat is told that the request already succeeded.
func TestAnswerTooBigToKeepIsNotRunAgain(t *testing.T) {
	var runs atomic.Int32
	i := NewIdempotency()
	h := i.Wrap(bigAnswer(idempotencyMaxBody+1, &runs))
	first := idemDo(h, idemReq("/api/sessions/launch-team", `{}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, 200, first.Code)
	assert.Equal(t, idempotencyMaxBody+1, first.Body.Len(), "the first caller got the whole answer")
	again := idemDo(h, idemReq("/api/sessions/launch-team", `{}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, http.StatusConflict, again.Code)
	assert.Contains(t, again.Body.String(), "request_already_done")
	assert.EqualValues(t, 1, runs.Load(), "it started once")
	assert.Zero(t, i.bytes, "nothing was kept")
	other := idemDo(h, idemReq("/api/sessions/launch-team", `{"b":1}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, http.StatusUnprocessableEntity, other.Code, "another body is still a reuse")
}

// #5855 (coral-hub's review): the in-progress record is gone on EVERY way a handler can end. A success becomes the stored answer; a refusal, a failure, a handler that returned without
// answering, a panic and a request whose client went away mid-launch (its context cancelled) either keep the answer (a success) or free the key (anything else).
func TestTheRunningRecordEndsOnEveryExitOfTheHandler(t *testing.T) {
	type exit struct {
		name   string
		cancel bool // the client goes away while the handler runs
		do     func(w http.ResponseWriter)
		kept   bool // the answer is stored (a repeat is a replay); otherwise the key is free (a repeat runs)
		panics bool
	}
	ok := func(w http.ResponseWriter) { w.WriteHeader(200); _, _ = w.Write([]byte(`{"ok":true}`)) }
	for _, tc := range []exit{
		{name: "2xx", do: ok, kept: true},
		{name: "a refusal", do: func(w http.ResponseWriter) { w.WriteHeader(403) }},
		{name: "a failure", do: func(w http.ResponseWriter) { w.WriteHeader(500) }},
		{name: "no answer at all", do: func(w http.ResponseWriter) {}},
		{name: "a panic", panics: true},
		{name: "the client gone, the launch still succeeded", cancel: true, do: ok, kept: true},
		{name: "the client gone, the handler failed", cancel: true, do: func(w http.ResponseWriter) { w.WriteHeader(503) }},
		{name: "the client gone, nothing answered", cancel: true, do: func(w http.ResponseWriter) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := NewIdempotency()
			var runs atomic.Int32
			started := make(chan struct{})
			h := i.Wrap(func(w http.ResponseWriter, r *http.Request) {
				if runs.Add(1) > 1 {
					ok(w) // the retry
					return
				}
				if tc.cancel {
					close(started)
					<-r.Context().Done() // the client left: the handler goes on to its end, as a launch does
				}
				if tc.panics {
					panic("boom")
				}
				tc.do(w)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey).WithContext(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() { _ = recover() }()
				idemDo(h, req)
			}()
			if tc.cancel {
				<-started
				cancel()
			}
			<-done
			assert.Len(t, i.entries, map[bool]int{true: 1, false: 0}[tc.kept], "the record after the exit: kept only for a success")
			again := idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey))
			if tc.kept {
				assert.Equal(t, "true", again.Header().Get("Idempotent-Replay"), "a success is answered again")
				assert.EqualValues(t, 1, runs.Load())
			} else {
				assert.Empty(t, again.Header().Get("Idempotent-Replay"))
				assert.Equal(t, 200, again.Code, "the key is free: the retry ran, not 409")
				assert.EqualValues(t, 2, runs.Load())
			}
		})
	}
}

// A running record has its own bound: a handler that never returns does not keep every retry on 409; after the bound the key is free (and the late handler, if it ever returns,
// does not touch the new record).
func TestARunningRecordIsFreeAfterItsBound(t *testing.T) {
	i := NewIdempotency()
	clock := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	i.now = func() time.Time { return clock }
	release, started := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	h := i.Wrap(func(w http.ResponseWriter, r *http.Request) {
		if runs.Add(1) == 1 {
			close(started)
			<-release // never returns during the test
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"run":` + strconv.Itoa(int(runs.Load())) + `}`))
	})
	stuck := make(chan *httptest.ResponseRecorder, 1)
	go func() { stuck <- idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey)) }()
	<-started
	clock = clock.Add(idempotencyRunningMax - time.Second)
	assert.Equal(t, http.StatusConflict, idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey)).Code, "still within the bound: in progress")
	clock = clock.Add(2 * time.Second)
	rec := idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, 200, rec.Code, "past the bound the key is free: the retry ran")
	assert.EqualValues(t, 2, runs.Load())
	close(release) // the late handler returns now: it must not overwrite or drop the new record
	<-stuck
	again := idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", idemKey))
	assert.Equal(t, "true", again.Header().Get("Idempotent-Replay"), "the retry's answer stands")
	assert.EqualValues(t, 2, runs.Load())
}

// The bound is chosen from the longest a launch route may run: the lifecycle bound plus a member's share for a team of 60, and under the time to live.
func TestTheRunningBoundCoversALegitimateTeamLaunch(t *testing.T) {
	longest := lifecycleTimeout + 60*lifecyclePerSession
	assert.GreaterOrEqual(t, idempotencyRunningMax, longest, "a launch of a team of 60 is still running at the bound")
	assert.Less(t, idempotencyRunningMax, idempotencyTTL, "and the bound is under the time to live")
}

// teamBody is a launch-team body with n members.
func teamBody(n int) string {
	agents := make([]string, n)
	for k := range agents {
		agents[k] = `{"name":"a` + strconv.Itoa(k) + `"}`
	}
	return `{"board_name":"t","agents":[` + strings.Join(agents, ",") + `]}`
}

// #5859 (Security's note on #5855): a team launch that is bigger than the 8-minute bound covers gets a bound from its own member count, the time its handler is given
// (lifecycleTimeout plus lifecyclePerSession each) and a minute; every other route and a body that does not parse get the least; nothing passes the cap.
func TestRunningBoundGrowsWithATeamLaunchesMemberCount(t *testing.T) {
	team := func(n int) time.Duration { return runningBound("/api/sessions/launch-team", []byte(teamBody(n))) }
	assert.Equal(t, idempotencyRunningMax, team(1))
	assert.Equal(t, idempotencyRunningMax, team(60), "a team of 60: the least")
	assert.Equal(t, lifecycleTimeout+72*lifecyclePerSession+time.Minute, team(72), "72 members: what its handler is given and a minute")
	assert.Greater(t, team(100), idempotencyRunningMax, "100 members need longer than 8 minutes")
	assert.Equal(t, lifecycleTimeout+100*lifecyclePerSession+time.Minute, team(100))
	assert.Equal(t, idempotencyRunningCap, team(5000), "never more than the cap")
	for _, path := range []string{"/api/sessions/launch", "/api/sessions/live/x/resume", "/api/sessions/live/x/wake", "/api/tasks/run"} {
		assert.Equal(t, idempotencyRunningMax, runningBound(path, []byte(teamBody(100))), path+": only a team launch is by its members")
	}
	assert.Equal(t, idempotencyRunningMax, runningBound("/api/sessions/launch-team", []byte(`not json`)))
	assert.Equal(t, idempotencyRunningMax, runningBound("/api/sessions/launch-team", []byte(`{"agents":"x"}`)))
	assert.Equal(t, idempotencyRunningMax, runningBound("/api/sessions/launch-team", nil))
	assert.Less(t, idempotencyRunningMax, idempotencyRunningCap)
}

// The record of a big team's launch is held for that longer bound: a retry within it is still "in progress", after it the key is free.
func TestABigTeamLaunchKeepsItsKeyBusyForItsOwnBound(t *testing.T) {
	i := NewIdempotency()
	clock := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	i.now = func() time.Time { return clock }
	release, started := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	h := i.Wrap(func(w http.ResponseWriter, r *http.Request) {
		if runs.Add(1) == 1 {
			close(started)
			<-release
		}
		w.WriteHeader(200)
	})
	body := teamBody(100)
	bound := lifecycleTimeout + 100*lifecyclePerSession + time.Minute
	stuck := make(chan struct{})
	go func() {
		defer close(stuck)
		idemDo(h, idemReq("/api/sessions/launch-team", body, "hub-user-1234abcd", idemKey))
	}()
	<-started
	clock = clock.Add(idempotencyRunningMax + time.Second) // past the least bound, within this team's
	assert.Equal(t, http.StatusConflict, idemDo(h, idemReq("/api/sessions/launch-team", body, "hub-user-1234abcd", idemKey)).Code, "a 100-member team may still be starting")
	clock = clock.Add(bound - idempotencyRunningMax) // past its own bound
	assert.Equal(t, 200, idemDo(h, idemReq("/api/sessions/launch-team", body, "hub-user-1234abcd", idemKey)).Code, "past its own bound the key is free")
	assert.EqualValues(t, 2, runs.Load())
	close(release)
	<-stuck
}

// #5867 (QA's note on #5859): the purge that runs when a request begins also drops the running records past their own bound, so handlers that never return do not keep
// their per-user slots (and their share of the 2000) until each of their keys happens to be used again.
func TestPurgeFreesRunningRecordsPastTheirBound(t *testing.T) {
	i := NewIdempotency()
	clock := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	i.now = func() time.Time { return clock }
	const stuck = 5
	release := make(chan struct{})
	started := make(chan struct{}, stuck)
	var runs atomic.Int32
	h := i.Wrap(func(w http.ResponseWriter, r *http.Request) {
		if runs.Add(1) <= stuck {
			started <- struct{}{}
			<-release
		}
		w.WriteHeader(200)
	})
	var wg sync.WaitGroup
	for n := 0; n < stuck; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", "0123456789abcdef-stuck-"+strconv.Itoa(n)))
		}(n)
	}
	for n := 0; n < stuck; n++ {
		<-started
	}
	i.mu.Lock()
	assert.Equal(t, stuck, i.perUser["hub-user-1234abcd"], "five running records hold five slots")
	i.mu.Unlock()
	clock = clock.Add(idempotencyRunningMax + time.Second) // past their bound, and no one asks for those keys again
	assert.Equal(t, 200, idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", "0123456789abcdef-fresh-1")).Code, "another key, from the same user")
	i.mu.Lock()
	assert.Equal(t, 1, i.perUser["hub-user-1234abcd"], "the stuck records were purged: only the new one holds a slot")
	assert.Len(t, i.entries, 1)
	i.mu.Unlock()
	close(release) // the late handlers return: they find their records gone and leave the new one alone
	wg.Wait()
	i.mu.Lock()
	assert.Len(t, i.entries, 1)
	assert.Equal(t, 1, i.perUser["hub-user-1234abcd"])
	i.mu.Unlock()
	assert.Equal(t, "true", idemDo(h, idemReq("/api/sessions/launch", `{}`, "hub-user-1234abcd", "0123456789abcdef-fresh-1")).Header().Get("Idempotent-Replay"), "the new record still stands")
}
