package routes

import (
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/httputil"
)

// A shared write budget per agent session (#4602, the Orchestrator's decision 3): the routes through which an agent token can add rows,
// messages and panels in a loop (a board post, a task, a notification, a wait, an agent UI panel or event) draw from ONE bucket per
// verified session, so five ad-hoc limiters are not needed and a looping agent runs out after a burst, not after it has filled the
// database. Only agent-token requests are counted: the Operator, the command line, the API key and Coral Hub callers are never limited,
// and neither are the hooks' state events, token-usage reports, reads or the artifact upload (which has its own window).
//
// The size is generous against real traffic: the busiest sender of 61,000 messages on the team board (outside one test fixture that
// posted 150 in a minute) did 25 posts in a minute; the bucket is 240 a minute with a burst of 60.
const (
	agentWriteBurst     = 60
	agentWritePerSecond = 4.0 // 240 a minute
	agentWriteMaxIdle   = 10 * time.Minute
	agentWriteMaxKeys   = 10000
)

var agentWriteRoutes = []struct {
	method string
	path   *regexp.Regexp
}{
	{http.MethodPost, regexp.MustCompile(`^/api/board/[^/]+/messages$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/board/[^/]+/tasks$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/board/[^/]+/tasks/[^/]+/reply$`)}, // a reply is a post: the same budget (#5463)
	{http.MethodPost, regexp.MustCompile(`^/api/agent/tasks$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/board/[^/]+/waits$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/notifications$`)},
	{http.MethodPut, regexp.MustCompile(`^/api/agent/ui/[^/]+$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/agent/ui/[^/]+/events$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/agent/ui-request$`)},
}

type writeBucket struct {
	tokens float64
	at     time.Time
}

// agentWriteLimiter holds one token bucket per agent session.
type agentWriteLimiter struct {
	mu      sync.Mutex
	buckets map[string]*writeBucket
	now     func() time.Time
	pruned  time.Time
}

func newAgentWriteLimiter() *agentWriteLimiter {
	return &agentWriteLimiter{buckets: map[string]*writeBucket{}, now: time.Now}
}

// take spends one token of the session's bucket and says how long to wait when there is none.
func (l *agentWriteLimiter) take(session string) (ok bool, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.buckets) > 1000 && now.Sub(l.pruned) > time.Minute {
		l.prune(now)
	}
	b := l.buckets[session]
	if b == nil {
		if len(l.buckets) >= agentWriteMaxKeys {
			l.prune(now)
			if len(l.buckets) >= agentWriteMaxKeys {
				for k := range l.buckets { // still full of busy sessions: make room for one, any one
					delete(l.buckets, k)
					break
				}
			}
		}
		b = &writeBucket{tokens: agentWriteBurst, at: now}
		l.buckets[session] = b
	}
	b.tokens = math.Min(agentWriteBurst, b.tokens+now.Sub(b.at).Seconds()*agentWritePerSecond)
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / agentWritePerSecond * float64(time.Second))
}

// prune drops the buckets idle for agentWriteMaxIdle (they are full again anyway).
func (l *agentWriteLimiter) prune(now time.Time) {
	l.pruned = now
	for k, b := range l.buckets {
		if now.Sub(b.at) > agentWriteMaxIdle {
			delete(l.buckets, k)
		}
	}
}

// AgentWriteLimit is the middleware: after the identity layer (which says which session a token is), before the handlers.
func AgentWriteLimit() func(http.Handler) http.Handler {
	l := newAgentWriteLimiter()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if owner, ok := VerifiedSession(r.Context()); ok && owner.SessionID != "" && limitedAgentWrite(r) {
				if ok, wait := l.take(owner.SessionID); !ok {
					secs := max(int(math.Ceil(wait.Seconds())), 1)
					msg := fmt.Sprintf("This agent is writing faster than %d a minute: slow down and retry in %d s.", int(agentWritePerSecond*60), secs)
					// A limit on this sender's writes, not a busy server (httputil.ActionLimitHeader: the dashboard's overload logic and the
					// Hub treat an unmarked 429 as Coral being overloaded), with "message" for coral-board, which prints it before the code.
					httputil.WriteActionLimitBody(w, secs, map[string]any{"error": msg, "message": msg, "code": "rate_limited", "not_sent": true})
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// limitedAgentWrite says whether the request is one of the budgeted writes. The path is matched in both spellings: chi routes on the RAW path
// when the request has one, so "/api/board/a%2Fb/messages" reaches the messages handler (project "a%2Fb") although its decoded path
// "/api/board/a/b/messages" has one more segment than the pattern: matching only the decoded path would let such a request past the budget.
func limitedAgentWrite(r *http.Request) bool {
	for _, rt := range agentWriteRoutes {
		if r.Method == rt.method && (rt.path.MatchString(r.URL.Path) || rt.path.MatchString(r.URL.EscapedPath())) {
			return true
		}
	}
	return false
}
