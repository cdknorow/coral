package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cliauth"
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
	callerCLISigned     = "cli_signed"  // a command-line signature (#3418): the local Operator
	callerOpen          = "open"        // the liveness check and the command line's hello
	mismatchCrossBoard  = "cross_board"
	mismatchImpersonate = "impersonation"
	mismatchOperator    = "operator"
	identityLogInterval = 5 * time.Minute
	identityRecentMax   = 50
	// identityBreakKeysMax bounds the would-break breakdown (distinct caller, tool and
	// route triples); later new triples are counted under "other".
	identityBreakKeysMax = 200
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

	// Strict local auth (#3418), set once before serving (SetStrict).
	strict    bool
	validKey  func(*http.Request) bool                  // a valid API key in the request
	verifyCLI func(*http.Request) (*http.Request, bool) // a valid command-line signature
	otherUser func(*http.Request) bool                  // a loopback caller running as another OS user

	mu         sync.Mutex
	callers    map[string]int64
	bySession  map[string]int64 // token-attributed requests per session name
	byTool     map[string]int64 // token-attributed requests per tool (User-Agent name)
	mismatches map[string]int64
	// breakBy counts would-break requests by "caller tool METHOD /route/{pattern}" (#3418):
	// which callers strict mode would refuse, and where.
	breakBy map[string]int64
	// operatorCalls counts calls by agents to the Operator-only routes (#3424), by
	// "session METHOD /route": refused with strict local auth, recorded otherwise.
	operatorCalls map[string]int64
	// refused counts strict-mode refusals by code (#3418): zero in a healthy team.
	refused map[string]int64
	recent  []IdentityMismatch
	lastLog map[string]time.Time
	now     func() time.Time
}

// NewIdentityObserver returns the observer; hasSession reports a valid dashboard cookie.
func NewIdentityObserver(db *store.DB, bs *board.Store, hasSession func(*http.Request) bool) *IdentityObserver {
	return &IdentityObserver{db: db, bs: bs, hasSession: hasSession,
		callers: map[string]int64{}, bySession: map[string]int64{}, byTool: map[string]int64{},
		mismatches: map[string]int64{}, breakBy: map[string]int64{}, refused: map[string]int64{}, operatorCalls: map[string]int64{}, lastLog: map[string]time.Time{}, now: time.Now}
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

// SetVerifiers gives the observer the credential checks it applies in every mode:
// validKey checks an API key; verifyCLI checks a command-line signature (and uses up its
// nonce).
func (o *IdentityObserver) SetVerifiers(validKey func(*http.Request) bool, verifyCLI func(*http.Request) (*http.Request, bool)) {
	o.validKey, o.verifyCLI = validKey, verifyCLI
}

// SetOtherUser gives the observer the peer check: a signed request from another OS user is
// refused 403 other_user (as the cloud routes say), not 401.
func (o *IdentityObserver) SetOtherUser(f func(*http.Request) bool) { o.otherUser = f }

// SetStrict turns on strict local auth (#3418): requests from this computer that strict
// mode would refuse are refused instead of counted.
func (o *IdentityObserver) SetStrict() { o.strict = true }

// Strict reports whether strict local auth is on.
func (o *IdentityObserver) Strict() bool { return o != nil && o.strict }

// Middleware classifies every request. In phase 1 (log only) it never refuses one; with
// strict local auth it refuses what the report lists as would-break.
func (o *IdentityObserver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o == nil {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/ws/") {
			if o.strict && o.lockPage(w, r) {
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if token := r.Header.Get(store.SessionTokenHeader); token != "" {
			owner, err := o.db.SessionTokenOwnerOf(r.Context(), token)
			if err != nil {
				o.count(callerAgentBadToken)
				o.logOnce("bad_token:"+r.UserAgent(), "[identity] request with an unknown or revoked session token", "request", r.Method+" "+r.URL.Path, "tool", r.UserAgent(), "strict", o.strict)
				if o.strict {
					o.countBreak(callerAgentBadToken, r)
					o.refuse(w, http.StatusUnauthorized, "session_token_required",
						"This agent's session token is unknown or no longer valid (the agent was restarted or closed). Restart the agent from Coral.")
					return
				}
				next.ServeHTTP(w, r)
				o.countBreak(callerAgentBadToken, r)
				return
			}
			o.count(callerAgentToken)
			o.attribute(owner.SessionName, r.UserAgent())
			if o.strict {
				// The token is the whole identity: a command-line signature on the same
				// request never adds the Operator's authority (#3418).
				r.Header.Del(cliauth.CLIAuthHeader)
			}
			r = r.WithContext(context.WithValue(r.Context(), verifiedSessionKey{}, owner))
			if route, ok := operatorOnlyRoute(r); ok {
				o.countOperatorCall(owner.SessionName, r.Method+" "+route)
				if o.strict {
					o.refuse(w, http.StatusForbidden, "forbidden_for_agent",
						"An agent can't "+operatorOnlyRoutes[r.Method+" "+route]+": that is for the Operator (the dashboard, or the command line outside an agent)")
					return
				}
			}
			if m := o.checkBoard(r, owner); m != nil && o.strict {
				o.refuse(w, http.StatusForbidden, "identity_mismatch",
					"This agent can't act as "+strconv.Quote(m.Claimed)+" ("+m.Field+"): its session is "+strconv.Quote(m.Expected)+" ("+m.Kind+")")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		caller := o.classify(r)
		o.count(caller)
		if caller == callerAgentNoToken {
			if route, ok := operatorOnlyRoute(r); ok {
				o.countOperatorCall("(no token)", r.Method+" "+route)
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), callerKindKey{}, caller))
		// In every mode (#3421 v2, operator decision) a credential that is PRESENT must be
		// valid: a command-line signature, an API key, a Coral Hub stamp. A wrong one is
		// refused, never ignored. A request with no credential at all keeps working outside
		// strict mode (counted in the identity report as local_other); with strict local
		// auth, every caller needs one.
		claimed := caller == callerCLISigned || caller == callerAPIKey || caller == callerHub
		if o.strict || claimed {
			r2, ok := o.admit(r, caller)
			if !ok {
				o.countBreak(caller, r)
				switch caller {
				case callerAgentNoToken:
					o.refuse(w, http.StatusUnauthorized, "session_token_required",
						"This tool runs without the agent's session token (an agent started before strict mode or before v1.4.19?). Restart the agent from Coral.")
				case callerCLISigned:
					if o.otherUser != nil && o.otherUser(r) {
						o.refuse(w, http.StatusForbidden, "other_user", "Only the user Coral runs as can use its command line")
						break
					}
					o.refuse(w, http.StatusUnauthorized, "cli_signature_required",
						"The command-line signature is not valid for this Coral (another data folder's token, or a used or expired signature)")
				case callerAPIKey, callerHub:
					o.refuse(w, http.StatusUnauthorized, "invalid_credential",
						"The credential sent is not valid for this Coral (a wrong API key, or a Coral Hub stamp that isn't this connector's): it is refused, never ignored")
				default:
					o.refuse(w, http.StatusUnauthorized, "dashboard_auth_required",
						"Coral is locked: open it from the Coral app, or run 'coral open' to sign in this browser")
				}
				return
			}
			next.ServeHTTP(w, r2)
			return
		}
		next.ServeHTTP(w, r)
		if wouldBreak[caller] {
			o.countBreak(caller, r)
		}
	})
}

// operatorOnlyRoutes run code or start agents on this computer (#3424, audit HIGH-01):
// workflows (shell steps), scheduled jobs and task runs. With strict local auth an agent's
// token can't use them (the Operator can: the dashboard, an API key, the signed command
// line); otherwise each agent call is recorded in the identity report.
var operatorOnlyRoutes = map[string]string{
	"POST /api/workflows":                        "create a workflow",
	"PUT /api/workflows/{workflowID}":            "change a workflow",
	"DELETE /api/workflows/{workflowID}":         "delete a workflow",
	"POST /api/workflows/{workflowID}/trigger":   "run a workflow",
	"POST /api/workflows/by-name/{name}/trigger": "run a workflow",
	"POST /api/scheduled/jobs":                   "schedule a job",
	"PUT /api/scheduled/jobs/{jobID}":            "change a scheduled job",
	"DELETE /api/scheduled/jobs/{jobID}":         "delete a scheduled job",
	"POST /api/scheduled/jobs/{jobID}/toggle":    "turn a scheduled job on or off",
	"POST /api/tasks/run":                        "start a task run",
}

// operatorOnlyRoute returns the route pattern of r when it is one of operatorOnlyRoutes,
// asking the router (the middleware runs before routing).
func operatorOnlyRoute(r *http.Request) (string, bool) {
	if !strings.HasPrefix(r.URL.Path, "/api/workflows") && !strings.HasPrefix(r.URL.Path, "/api/scheduled/") && r.URL.Path != "/api/tasks/run" {
		return "", false
	}
	rc := chi.RouteContext(r.Context())
	if rc == nil || rc.Routes == nil {
		return "", false
	}
	tctx := chi.NewRouteContext()
	if !rc.Routes.Match(tctx, r.Method, r.URL.Path) {
		return "", false
	}
	route := tctx.RoutePattern()
	_, ok := operatorOnlyRoutes[r.Method+" "+route]
	return route, ok
}

func (o *IdentityObserver) countOperatorCall(session, route string) {
	o.mu.Lock()
	key := session + " " + route
	if _, ok := o.operatorCalls[key]; !ok && len(o.operatorCalls) >= identityBreakKeysMax {
		key = "other"
	}
	o.operatorCalls[key]++
	o.mu.Unlock()
	o.logOnce("operator_route:"+session, "[identity] an agent called an Operator-only route", "session", session, "route", route, "strict", o.strict)
}

// strictOpenPaths need no credential in strict mode: the liveness check and the command
// line's hello (step 1 of its signature, which proves the server, not the caller).
var strictOpenPaths = map[string]bool{"GET /api/health": true, "HEAD /api/health": true, "POST " + cliauth.CLIHelloPath: true}

// admit applies strict local auth to a request with no session token: the request (with
// what was verified recorded on it) and whether it may go on.
func (o *IdentityObserver) admit(r *http.Request, caller string) (*http.Request, bool) {
	if strictOpenPaths[r.Method+" "+r.URL.Path] {
		return r, true
	}
	switch auth.MethodFrom(r.Context()) {
	case auth.MethodAPIKey, auth.MethodSession:
		return r, true // another computer, checked by the auth middleware
	}
	if cloud.TunneledAuthentic(r) {
		return r, true // Coral Hub: the tunnel's own rules (a forged stamp is not this)
	}
	if r.Header.Get(cliauth.CLIAuthHeader) != "" && o.verifyCLI != nil {
		if r2, ok := o.verifyCLI(r); ok {
			return r2, true // the person at the command line: the local Operator
		}
		return r, false
	}
	if caller == callerAgentNoToken {
		return r, false // never promoted: an agent tool needs its own token
	}
	if o.hasSession != nil && o.hasSession(r) {
		return r, true
	}
	if o.validKey != nil && o.validKey(r) {
		return r, true
	}
	return r, false
}

// lockPage sends a page load without the dashboard's sign-in to /auth (#3418), the way
// the API-key flow does, so the Coral app signs in again by itself. It reports whether it
// answered.
func (o *IdentityObserver) lockPage(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if p == "/auth" || strings.HasPrefix(p, "/auth/") || strings.HasPrefix(p, "/static/") || p == "/manifest.json" || p == "/favicon.ico" {
		return false
	}
	switch auth.MethodFrom(r.Context()) {
	case auth.MethodAPIKey, auth.MethodSession:
		return false
	}
	if cloud.TunneledAuthentic(r) || (o.hasSession != nil && o.hasSession(r)) || (o.validKey != nil && o.validKey(r)) {
		return false
	}
	http.Redirect(w, r, "/auth?locked=1&return_to="+url.QueryEscape(auth.SafeReturnTo(r.URL.RequestURI())), http.StatusTemporaryRedirect)
	return true
}

// refuse answers a strict-mode refusal and counts it by code.
func (o *IdentityObserver) refuse(w http.ResponseWriter, status int, code, msg string) {
	o.mu.Lock()
	o.refused[code]++
	o.mu.Unlock()
	writeJSON(w, status, map[string]any{"error": msg, "code": code})
}

// callerKindKey holds a token-less request's caller type (classify) in its context.
type callerKindKey struct{}

// humanInputCaller reports whether a request that types into an agent comes from
// a person: the dashboard (signed in, or this computer's browser), Coral Hub (its
// tunnel admits input only at Interact) or the operator's signed command line.
// An agent's token or tool, an API key or any other caller is automation, and its
// input goes through the automated-input gate (#3431). Unclassified (no observer
// in the chain) counts as automation: the gate then types at once unless a
// prompt is open.
func humanInputCaller(r *http.Request) bool {
	if r.Header.Get(store.SessionTokenHeader) != "" {
		return false
	}
	if _, ok := r.Context().Value(verifiedSessionKey{}).(*store.SessionTokenOwner); ok {
		return false
	}
	switch kind, _ := r.Context().Value(callerKindKey{}).(string); kind {
	case callerDashCookie, callerDashNoCookie:
		return true
	case callerHub:
		// Only the connector's own per-process stamp: any process can send an
		// X-Coral-Via-Tunnel header (Backend's #3431 review).
		return cloud.TunneledAuthentic(r)
	case callerCLISigned:
		// Only a signature the middleware checked: any process can send the
		// header. Unchecked (strict mode off), the CLI's input is gated like a
		// script's, which still types at once while no prompt is open.
		return cliauth.Verified(r)
	}
	return false
}

// classify names the caller of a request that carries no session token.
func (o *IdentityObserver) classify(r *http.Request) string {
	switch {
	case strictOpenPaths[r.Method+" "+r.URL.Path]:
		return callerOpen
	case r.Header.Get(cliauth.CLIAuthHeader) != "":
		return callerCLISigned
	case isAgentTool(r):
		return callerAgentNoToken
	case cloud.Tunneled(r):
		return callerHub
	case r.Header.Get("X-API-Key") != "" || r.URL.Query().Get("api_key") != "" || strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer "):
		return callerAPIKey
	case o.hasSession != nil && o.hasSession(r):
		return callerDashCookie
	case sameOriginFetch(r) && (r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Sec-Fetch-Site") != ""):
		return callerDashNoCookie
	}
	return callerLocalOther
}

// countBreak records where a would-break request went: the caller type, the tool (the
// User-Agent's product name) and the route pattern chi matched (after routing, so ids
// don't multiply the keys). Never the query or any identity value.
func (o *IdentityObserver) countBreak(caller string, r *http.Request) {
	tool, _, _ := strings.Cut(r.UserAgent(), "/")
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "-"
	}
	if len(tool) > 40 {
		tool = tool[:40]
	}
	route := ""
	if rc := chi.RouteContext(r.Context()); rc != nil {
		route = rc.RoutePattern()
		if route == "" && rc.Routes != nil {
			// Refused before routing (strict mode): ask the router which route it was.
			if tctx := chi.NewRouteContext(); rc.Routes.Match(tctx, r.Method, r.URL.Path) {
				route = tctx.RoutePattern()
			}
		}
	}
	if route == "" {
		route = "(unrouted)"
	}
	key := caller + " " + tool + " " + r.Method + " " + route
	o.mu.Lock()
	if _, ok := o.breakBy[key]; !ok && len(o.breakBy) >= identityBreakKeysMax {
		key = caller + " other"
	}
	o.breakBy[key]++
	o.mu.Unlock()
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

// checkBoard compares a board request's claimed identity with the token's session, and
// returns the first mismatch (nil when there is none). Every mismatch is recorded.
func (o *IdentityObserver) checkBoard(r *http.Request, owner *store.SessionTokenOwner) (first *IdentityMismatch) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/board/")
	if !ok || o.bs == nil {
		return nil
	}
	project, _, _ := strings.Cut(rest, "/")
	switch project {
	case "", "projects", "tasks", "remotes":
		return nil
	}
	sub, err := o.bs.GetSubscriptionBySessionName(r.Context(), owner.SessionName)
	if err != nil || sub == nil {
		return nil // not on a board yet (it may be subscribing now)
	}
	note := func(m IdentityMismatch) {
		if first == nil {
			first = &m
		}
	}
	if !strings.EqualFold(project, sub.Project) {
		note(o.mismatch(r, owner, mismatchCrossBoard, sub.Project, project, "project"))
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
				note(o.mismatch(r, owner, mismatchOperator, sub.SubscriberID, v, f))
			case f == "as" && strings.EqualFold(v, sub.JobTitle):
				// 'as' is a display name for auto-subscribe: the session's own role is fine
			default:
				note(o.mismatch(r, owner, mismatchImpersonate, sub.SubscriberID, v, f))
			}
		}
	}
	return first
}

func (o *IdentityObserver) mismatch(r *http.Request, owner *store.SessionTokenOwner, kind, expected, claimed, field string) IdentityMismatch {
	m := IdentityMismatch{At: o.now().UTC().Format(time.RFC3339), Kind: kind, Session: owner.SessionName,
		Expected: expected, Claimed: claimed, Field: field, Request: r.Method + " " + r.URL.Path}
	o.mu.Lock()
	o.mismatches[kind]++
	o.recent = append(o.recent, m)
	if len(o.recent) > identityRecentMax {
		o.recent = o.recent[len(o.recent)-identityRecentMax:]
	}
	o.mu.Unlock()
	msg := "[identity] mismatch (log only)"
	if o.strict {
		msg = "[identity] mismatch refused (strict local auth)"
	}
	o.logOnce("mismatch:"+owner.SessionName+":"+kind, msg,
		"kind", kind, "session", owner.SessionName, "expected", expected, "claimed", claimed, "field", field, "request", m.Request)
	return m
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
	breakBy := map[string]int64{}
	for k, v := range o.breakBy {
		breakBy[k] = v
	}
	refused := map[string]int64{}
	for k, v := range o.refused {
		refused[k] = v
	}
	opCalls := map[string]int64{}
	for k, v := range o.operatorCalls {
		opCalls[k] = v
	}
	recent := append([]IdentityMismatch(nil), o.recent...)
	sessions, tools := map[string]int64{}, map[string]int64{}
	for k, v := range o.bySession {
		sessions[k] = v
	}
	for k, v := range o.byTool {
		tools[k] = v
	}
	mode := "log_only"
	if o.strict {
		mode = "strict"
	}
	return map[string]any{"mode": mode, "callers": callers, "would_break_in_strict_mode": breaking, "would_break_by_route": breakBy, "refused": refused, "agent_operator_calls": opCalls,
		"mismatches": mm, "recent_mismatches": recent, "attributed_by_session": sessions, "attributed_by_tool": tools}
}
