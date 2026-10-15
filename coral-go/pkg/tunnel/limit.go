package tunnel

import (
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

// Hub request limits (#3624, hub audit H5 / rendering report F5). Every request Coral Hub sends
// passes through LocalHandler, so one limiter there bounds what a hub viewer can hold open on this
// computer; the local dashboard never goes through LocalHandler, so it can never be starved.
//
//   - long-lived routes (board long polls, peek, every WebSocket) have their own small pool, so
//     held polls never take the slots normal reads need;
//   - every other request takes a slot in the global pool, and the search routes also take one
//     in a small cost pool;
//   - a full pool answers at once (429, Retry-After, X-Coral-Limit: connector), never queues;
//   - a non-stream request that has no response headers after RequestDeadline is cancelled, so
//     a stalled handler can't hold its slot.
const (
	DefaultGlobalInFlight  = 64
	DefaultLongInFlight    = 16
	DefaultSearchInFlight  = 4
	DefaultRequestDeadline = 30 * time.Second
	// busyRetryAfter is the Retry-After a full pool answers with, in seconds.
	busyRetryAfter = "2"
	// LimitHeader marks Coral's own limit replies (the dashboard's X-Coral-Limit). "connector"
	// is a load limit, so the dashboard backs off on it ("action" would not).
	LimitHeader = "X-Coral-Limit"
)

// HubLimits configures LocalHandler's hub request limits. Zero values take the defaults.
type HubLimits struct {
	Global, Long, Search int
	RequestDeadline      time.Duration
}

type requestClass int

const (
	classGeneral requestClass = iota
	classSearch
	classLong
)

func (c requestClass) String() string {
	switch c {
	case classSearch:
		return "search"
	case classLong:
		return "long"
	}
	return "general"
}

var (
	longRoute   = regexp.MustCompile(`^/api/board/[^/]+/(waits/poll|peek)$`)
	searchRoute = regexp.MustCompile(`^(/api/sessions/history/search|/api/sessions/live/[^/]+/(history/search|search-files)|/api/board/[^/]+/messages/all|/api/token-usage(/.*)?)$`)
)

// classify puts a stream in its pool: every WebSocket and the board's long polls are long-lived,
// the search and full-history routes cost more, the rest is general.
func classify(kind Kind, decodedPath string) requestClass {
	switch {
	case kind == KindWS || longRoute.MatchString(decodedPath):
		return classLong
	case searchRoute.MatchString(decodedPath):
		return classSearch
	}
	return classGeneral
}

type limiter struct {
	global, long, search chan struct{}
	deadline             time.Duration
	rejected             atomic.Uint64
	lastLog              atomic.Int64 // unix seconds of the last rejection log line
}

func newLimiter(l HubLimits) *limiter {
	pick := func(v, def int) int {
		if v > 0 {
			return v
		}
		return def
	}
	d := l.RequestDeadline
	if d <= 0 {
		d = DefaultRequestDeadline
	}
	return &limiter{
		global:   make(chan struct{}, pick(l.Global, DefaultGlobalInFlight)),
		long:     make(chan struct{}, pick(l.Long, DefaultLongInFlight)),
		search:   make(chan struct{}, pick(l.Search, DefaultSearchInFlight)),
		deadline: d,
	}
}

func tryTake(c chan struct{}) bool {
	select {
	case c <- struct{}{}:
		return true
	default:
		return false
	}
}

// acquire takes the slots a class needs, or none; release gives them back.
func (lm *limiter) acquire(c requestClass) (release func(), ok bool) {
	switch c {
	case classLong:
		if !tryTake(lm.long) {
			return nil, false
		}
		return func() { <-lm.long }, true
	case classSearch:
		if !tryTake(lm.global) {
			return nil, false
		}
		if !tryTake(lm.search) {
			<-lm.global
			return nil, false
		}
		return func() { <-lm.search; <-lm.global }, true
	}
	if !tryTake(lm.global) {
		return nil, false
	}
	return func() { <-lm.global }, true
}

// refused counts a refusal and logs one line a minute at most, so a flood can't fill the log.
func (lm *limiter) refused(c requestClass, path string) {
	n := lm.rejected.Add(1)
	now := time.Now().Unix()
	if last := lm.lastLog.Load(); now-last >= 60 && lm.lastLog.CompareAndSwap(last, now) {
		slog.Warn("[tunnel] Coral Hub request refused: too many at once", "pool", c.String(), "path", path, "refused_total", n)
	}
}

// busy answers a stream whose pool is full: a 429 with Retry-After and the limit marker for an
// HTTP request; a WebSocket open is refused as too many streams (the hub answers 503, Retry-After).
func busy(s *Stream) {
	if s.Header.Kind == KindWS {
		s.Reject(CodeTooManyStreams, "too many requests from Coral Hub at once", 0)
		return
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Retry-After", busyRetryAfter)
	h.Set(LimitHeader, "connector")
	h.Set("Cache-Control", "no-store")
	_ = s.RespondWhole(ResponseHeader{Status: http.StatusTooManyRequests, Headers: h},
		[]byte(`{"error":"this computer is answering too many Coral Hub requests at once; try again shortly","code":"hub_busy"}`))
}

// limiterOnce lazily builds the limiter for handlers that never call init (tests building
// LocalHandler by hand still get one).
type limiterOnce struct {
	once sync.Once
	lm   *limiter
}

func (o *limiterOnce) get(l HubLimits) *limiter {
	o.once.Do(func() { o.lm = newLimiter(l) })
	return o.lm
}
