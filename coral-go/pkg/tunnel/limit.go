package tunnel

import (
	"log/slog"
	"net/http"
	"net/url"
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
//   - one Coral Hub user (the stream's HubUser) holds at most a share of each pool, so a few busy
//     viewers can't fill it for everyone (#3642); a stream without a user (an old hub) is held
//     only by the pools. The share is fairness between viewers, not a security boundary against
//     the hub: the hub names the user, so a bad hub can name as many as it likes. What bounds a
//     bad hub is the pools themselves, which hold whatever users it claims;
//   - a non-stream request that has no response headers after RequestDeadline is cancelled, so
//     a stalled handler can't hold its slot, and a reply body that makes no progress for
//     BodyIdle (nothing read from the local API, or the hub not taking it) is ended, so a
//     stalled reader can't either (#3642).
const (
	DefaultGlobalInFlight = 64
	// DefaultLongInFlight: every terminal through the Hub is one long-lived stream, and a dashboard
	// tab holds one more for its live feed (#3663: one user's tab and 12 terminals is normal use).
	DefaultLongInFlight    = 64
	DefaultSearchInFlight  = 4
	DefaultRequestDeadline = 30 * time.Second
	DefaultBodyIdle        = 60 * time.Second
	// busyRetryAfter is the Retry-After a full pool answers with, in seconds.
	busyRetryAfter = "2"
	// LimitHeader marks Coral's own limit replies (the dashboard's X-Coral-Limit). "connector"
	// is a load limit, so the dashboard backs off on it ("action" would not).
	LimitHeader = "X-Coral-Limit"
)

// HubLimits configures LocalHandler's hub request limits. Zero values take the defaults.
type HubLimits struct {
	Global, Long, Search int
	// UserGlobal, UserLong and UserSearch cap one Coral Hub user inside each pool. Zero: a
	// quarter of the general pool (16 of 64), half the long pool but at least 16 when the pool
	// allows (32 of 64: a tab and its terminals, #3663), half the search pool (2 of 4); never
	// less than 1.
	UserGlobal, UserLong, UserSearch int
	RequestDeadline                  time.Duration
	BodyIdle                         time.Duration
}

// The pools, as indexes of the limiter's counters.
const (
	poolGlobal = iota
	poolLong
	poolSearch
	poolCount
)

func (l HubLimits) size(pool int) int {
	pick := func(v, def int) int {
		if v > 0 {
			return v
		}
		return def
	}
	switch pool {
	case poolLong:
		return pick(l.Long, DefaultLongInFlight)
	case poolSearch:
		return pick(l.Search, DefaultSearchInFlight)
	}
	return pick(l.Global, DefaultGlobalInFlight)
}

// reserve is how many slots of a pool only streams without a Coral Hub user (the hub's own) may
// take (#3663): a sixteenth of the general and long pools (4 of 64), at least 1 while the pool
// has more than one slot; none in the search pool (the hub doesn't search).
func (l HubLimits) reserve(pool int) int {
	size := l.size(pool)
	if pool == poolSearch || size < 2 {
		return 0
	}
	return max(1, size/16)
}

func (l HubLimits) userCap(pool int) int {
	v, share := l.UserGlobal, 4
	switch pool {
	case poolLong:
		if l.UserLong > 0 {
			return l.UserLong
		}
		// A user's terminals: half the pool, and at least 16 (or the whole pool, if smaller).
		size := l.size(pool)
		return max(min(16, size), size/2)
	case poolSearch:
		v, share = l.UserSearch, 2
	}
	if v > 0 {
		return v
	}
	return max(1, l.size(pool)/share)
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
	searchRoute = regexp.MustCompile(`^(/api/sessions/history/search|/api/sessions/live/[^/]+/(history/search|search-files)|/api/board/[^/]+/messages/(all|search)|/api/token-usage(/.*)?)$`)
)

// eventsRoute is an agent's events (live) or a session's (history): a plain list is cheap (a limit, SQL filters), but with q the
// handler scans up to 200,000 events through the redactor, about 5 s of CPU (#4745, #4779).
var eventsRoute = regexp.MustCompile(`^/api/sessions/(history|live)/[^/]+/events$`)

// classify puts a stream in its pool: every WebSocket and the board's long polls are long-lived,
// the search and full-history routes cost more, the rest is general. rawQuery is the request's query string
// (already validated): the events routes are a search only when they carry a non-empty q.
func classify(kind Kind, decodedPath, rawQuery string) requestClass {
	switch {
	case kind == KindWS || longRoute.MatchString(decodedPath):
		return classLong
	case searchRoute.MatchString(decodedPath):
		return classSearch
	case eventsRoute.MatchString(decodedPath) && hasSearchText(rawQuery):
		return classSearch
	}
	return classGeneral
}

// hasSearchText reports whether the query string has a q with a value (a repeated q counts if any is set: the
// handler refuses it, but it is not a cheap request to answer first).
func hasSearchText(rawQuery string) bool {
	vals, _ := url.ParseQuery(rawQuery) // the pairs it could read: a bad escape drops that pair only
	for _, v := range vals["q"] {
		if v != "" {
			return true
		}
	}
	return false
}

// poolsOf lists the pools a class takes a slot in.
func poolsOf(c requestClass) []int {
	switch c {
	case classLong:
		return []int{poolLong}
	case classSearch:
		return []int{poolGlobal, poolSearch}
	}
	return []int{poolGlobal}
}

// limiter counts the slots in use, in all and per Coral Hub user. The sizes come with each
// acquire, so a changed setting applies to the next request; slots already held above a
// smaller size are kept until they end.
type limiter struct {
	mu       sync.Mutex
	inUse    [poolCount]int
	users    map[string]*[poolCount]int
	deadline time.Duration
	bodyIdle time.Duration
	rejected atomic.Uint64
	lastLog  atomic.Int64 // unix seconds of the last rejection log line
}

func newLimiter(l HubLimits) *limiter {
	d := l.RequestDeadline
	if d <= 0 {
		d = DefaultRequestDeadline
	}
	idle := l.BodyIdle
	if idle <= 0 {
		idle = DefaultBodyIdle
	}
	return &limiter{users: map[string]*[poolCount]int{}, deadline: d, bodyIdle: idle}
}

// acquire takes the slots a class needs for user ("" for none), or none; release gives them
// back. perUser says a refusal came from the user's own share, not a full pool.
func (lm *limiter) acquire(c requestClass, user string, lim HubLimits) (release func(), perUser, ok bool) {
	pools := poolsOf(c)
	lm.mu.Lock()
	defer lm.mu.Unlock()
	u := lm.users[user]
	for _, p := range pools {
		if lm.inUse[p] >= lim.size(p) {
			return nil, false, false
		}
		// Users' streams leave the last reserve(p) slots to the hub's own, which carry no user
		// (its feed of this machine's agents, its agent-list reads): users never crowd it out
		// (#3663). A hub that sends no user at all keeps the whole pool, as before.
		if user != "" && lm.inUse[p] >= lim.size(p)-lim.reserve(p) {
			return nil, false, false
		}
		if user != "" && u != nil && u[p] >= lim.userCap(p) {
			return nil, true, false
		}
	}
	if user != "" && u == nil {
		u = &[poolCount]int{}
		lm.users[user] = u
	}
	for _, p := range pools {
		lm.inUse[p]++
		if u != nil {
			u[p]++
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			lm.mu.Lock()
			defer lm.mu.Unlock()
			for _, p := range pools {
				lm.inUse[p]--
				if u != nil {
					u[p]--
				}
			}
			if u != nil && *u == [poolCount]int{} {
				delete(lm.users, user)
			}
		})
	}, false, true
}

// refused counts a refusal and logs one line a minute at most, so a flood can't fill the log.
func (lm *limiter) refused(c requestClass, path string, perUser bool) {
	n := lm.rejected.Add(1)
	now := time.Now().Unix()
	if last := lm.lastLog.Load(); now-last >= 60 && lm.lastLog.CompareAndSwap(last, now) {
		// logsafe-ok: a request's URL path, not a place on this computer
		slog.Warn("[tunnel] Coral Hub request refused: too many at once", "pool", c.String(), "one_user", perUser, "path", path, "refused_total", n)
	}
}

// The reasons a WebSocket refused for load carries (the StreamError message): the user's share
// of the pool, or the machine's whole pool (#3663). The hub maps each to a fixed text.
const (
	BusyUser    = "hub_busy:user"
	BusyMachine = "hub_busy:machine"
)

// busy answers a stream whose pool is full: a 429 with Retry-After and the limit marker for an
// HTTP request; a WebSocket open is refused as too many streams (the hub answers 503, Retry-After).
func busy(s *Stream, perUser bool) {
	if s.Header.Kind == KindWS {
		// A token, not text: the hub shows its own fixed text for each (#3663), so no string of
		// this machine's reaches a browser.
		token := BusyMachine
		if perUser {
			token = BusyUser
		}
		s.Reject(CodeTooManyStreams, token, 0)
		return
	}
	msg := `{"error":"this computer is answering too many Coral Hub requests at once; try again shortly","code":"hub_busy"}`
	if perUser {
		msg = `{"error":"you have too many Coral Hub requests open on this computer at once; try again shortly","code":"hub_busy"}`
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Retry-After", busyRetryAfter)
	h.Set(LimitHeader, "connector")
	h.Set("Cache-Control", "no-store")
	_ = s.RespondWhole(ResponseHeader{Status: http.StatusTooManyRequests, Headers: h},
		[]byte(msg))
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
