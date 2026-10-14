package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
)

// Identity phase 1 (#3387): LOG-ONLY. Every request is classified by who it comes
// from; an agent tool's request carries its session token (X-Coral-Session-Token,
// minted at launch), which names its session and so its board subscription. A board
// request whose claimed identity doesn't match is logged and counted, never refused.
// The report (GET /api/system/identity-report) says what strict mode would break.

// Caller types counted by the observer.
const (
	callerAgentToken    = "agent_token"     // an agent tool with a valid session token
	callerAgentBadToken = "agent_bad_token" // an unknown or revoked token
	callerAgentNoToken  = "agent_no_token"  // coral-board / coral-agent / a hook, no token
	callerDashCookie    = "dashboard_cookie"
	callerDashNoCookie  = "dashboard_no_cookie" // this computer's browser, no session cookie
	callerHub           = "coral_hub"           // through the Coral Hub tunnel
	callerAPIKey        = "api_key"
	callerLocalOther    = "local_other" // curl, scripts, the CLI without a token
	mismatchCrossBoard  = "cross_board"
	mismatchImpersonate = "impersonation"
	mismatchOperator    = "operator"
	identityLogInterval = 5 * time.Minute
	identityRecentMax   = 50
)

// wouldBreak are the caller types strict mode would refuse.
var wouldBreak = map[string]bool{callerAgentBadToken: true, callerAgentNoToken: true, callerDashNoCookie: true, callerLocalOther: true}

// IdentityMismatch is one logged mismatch (never the token).
type IdentityMismatch struct {
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Session  string `json:"session"`
	Expected string `json:"expected"`
	Claimed  string `json:"claimed"`
	Field    string `json:"field"`
	Request  string `json:"request"`
}

type verifiedSessionKey struct{}

// VerifiedSession is the agent session a request's token belongs to, if any.
func VerifiedSession(ctx context.Context) (*store.SessionTokenOwner, bool) {
	o, ok := ctx.Value(verifiedSessionKey{}).(*store.SessionTokenOwner)
	return o, ok
}

// IdentityObserver counts callers and logs identity mismatches.
type IdentityObserver struct {
	db         *store.DB
	bs         *board.Store
	hasSession func(*http.Request) bool

	mu         sync.Mutex
	callers    map[string]int64
	bySession  map[string]int64 // token-attributed requests per session name
	byTool     map[string]int64 // token-attributed requests per tool (User-Agent name)
	mismatches map[string]int64
	recent     []IdentityMismatch
	lastLog    map[string]time.Time
	now        func() time.Time
}

// NewIdentityObserver returns the observer; hasSession reports a valid dashboard cookie.
func NewIdentityObserver(db *store.DB, bs *board.Store, hasSession func(*http.Request) bool) *IdentityObserver {
	return &IdentityObserver{db: db, bs: bs, hasSession: hasSession,
		callers: map[string]int64{}, bySession: map[string]int64{}, byTool: map[string]int64{},
		mismatches: map[string]int64{}, lastLog: map[string]time.Time{}, now: time.Now}
}

var agentToolPrefixes = []string{"coral-board/", "coral-agent/", "coral-hook-"}

func isAgentTool(r *http.Request) bool {
	ua := r.UserAgent()
	for _, p := range agentToolPrefixes {
		if strings.HasPrefix(ua, p) {
			return true
		}
	}
	return false
}

// Middleware observes; it never refuses a request (phase 1).
func (o *IdentityObserver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o == nil || !strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/ws/") {
			next.ServeHTTP(w, r)
			return
		}
		if token := r.Header.Get(store.SessionTokenHeader); token != "" {
			owner, err := o.db.SessionTokenOwnerOf(r.Context(), token)
			if err != nil {
				o.count(callerAgentBadToken)
				o.logOnce("bad_token:"+r.UserAgent(), "[identity] request with an unknown or revoked session token", "request", r.Method+" "+r.URL.Path, "tool", r.UserAgent())
				next.ServeHTTP(w, r)
				return
			}
			o.count(callerAgentToken)
			o.attribute(owner.SessionName, r.UserAgent())
			r = r.WithContext(context.WithValue(r.Context(), verifiedSessionKey{}, owner))
			o.checkBoard(r, owner)
			next.ServeHTTP(w, r)
			return
		}
		switch {
		case isAgentTool(r):
			o.count(callerAgentNoToken)
		case cloud.Tunneled(r):
			o.count(callerHub)
		case r.Header.Get("X-API-Key") != "" || r.URL.Query().Get("api_key") != "" || strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer "):
			o.count(callerAPIKey)
		case o.hasSession != nil && o.hasSession(r):
			o.count(callerDashCookie)
		case sameOriginFetch(r) && (r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Sec-Fetch-Site") != ""):
			o.count(callerDashNoCookie)
		default:
			o.count(callerLocalOther)
		}
		next.ServeHTTP(w, r)
	})
}

// attribute counts a token-attributed request by session and by tool.
func (o *IdentityObserver) attribute(session, userAgent string) {
	tool, _, _ := strings.Cut(userAgent, "/")
	if tool == "" {
		tool = "unknown"
	}
	o.mu.Lock()
	o.bySession[session]++
	o.byTool[tool]++
	o.mu.Unlock()
}

func (o *IdentityObserver) count(caller string) {
	o.mu.Lock()
	o.callers[caller]++
	o.mu.Unlock()
}

func (o *IdentityObserver) logOnce(key, msg string, args ...any) {
	o.mu.Lock()
	now := o.now()
	last, seen := o.lastLog[key]
	if seen && now.Sub(last) < identityLogInterval {
		o.mu.Unlock()
		return
	}
	o.lastLog[key] = now
	o.mu.Unlock()
	slog.Warn(msg, args...)
}

// checkBoard compares a board request's claimed identity with the token's session.
func (o *IdentityObserver) checkBoard(r *http.Request, owner *store.SessionTokenOwner) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/board/")
	if !ok || o.bs == nil {
		return
	}
	project, _, _ := strings.Cut(rest, "/")
	switch project {
	case "", "projects", "tasks", "remotes":
		return
	}
	sub, err := o.bs.GetSubscriptionBySessionName(r.Context(), owner.SessionName)
	if err != nil || sub == nil {
		return // not on a board yet (it may be subscribing now)
	}
	if !strings.EqualFold(project, sub.Project) {
		o.mismatch(r, owner, mismatchCrossBoard, sub.Project, project, "project")
	}
	claims := map[string][]string{}
	for _, f := range []string{"subscriber_id", "as", "created_by"} {
		claims[f] = append(claims[f], r.URL.Query()[f]...)
	}
	if r.Body != nil && r.Body != http.NoBody {
		raw, err := io.ReadAll(r.Body)
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body map[string]json.RawMessage
		if err == nil && json.Unmarshal(raw, &body) == nil {
			for f := range claims {
				var s string
				if v, ok := body[f]; ok && json.Unmarshal(v, &s) == nil {
					claims[f] = append(claims[f], s)
				}
			}
		}
	}
	for f, vs := range claims {
		for _, v := range vs {
			v = strings.TrimSpace(v)
			switch {
			case v == "" || v == sub.SubscriberID:
			case strings.EqualFold(v, "Operator"):
				o.mismatch(r, owner, mismatchOperator, sub.SubscriberID, v, f)
			case f == "as" && strings.EqualFold(v, sub.JobTitle):
				// 'as' is a display name for auto-subscribe: the session's own role is fine
			default:
				o.mismatch(r, owner, mismatchImpersonate, sub.SubscriberID, v, f)
			}
		}
	}
}

func (o *IdentityObserver) mismatch(r *http.Request, owner *store.SessionTokenOwner, kind, expected, claimed, field string) {
	m := IdentityMismatch{At: o.now().UTC().Format(time.RFC3339), Kind: kind, Session: owner.SessionName,
		Expected: expected, Claimed: claimed, Field: field, Request: r.Method + " " + r.URL.Path}
	o.mu.Lock()
	o.mismatches[kind]++
	o.recent = append(o.recent, m)
	if len(o.recent) > identityRecentMax {
		o.recent = o.recent[len(o.recent)-identityRecentMax:]
	}
	o.mu.Unlock()
	o.logOnce("mismatch:"+owner.SessionName+":"+kind, "[identity] mismatch (log only)",
		"kind", kind, "session", owner.SessionName, "expected", expected, "claimed", claimed, "field", field, "request", m.Request)
}

// Report is the phase-1 identity report.
func (o *IdentityObserver) Report() map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	callers := map[string]int64{}
	breaking := map[string]int64{}
	for k, v := range o.callers {
		callers[k] = v
		if wouldBreak[k] {
			breaking[k] = v
		}
	}
	mm := map[string]int64{}
	for k, v := range o.mismatches {
		mm[k] = v
	}
	recent := append([]IdentityMismatch(nil), o.recent...)
	sessions, tools := map[string]int64{}, map[string]int64{}
	for k, v := range o.bySession {
		sessions[k] = v
	}
	for k, v := range o.byTool {
		tools[k] = v
	}
	return map[string]any{"mode": "log_only", "callers": callers, "would_break_in_strict_mode": breaking,
		"mismatches": mm, "recent_mismatches": recent, "attributed_by_session": sessions, "attributed_by_tool": tools}
}
