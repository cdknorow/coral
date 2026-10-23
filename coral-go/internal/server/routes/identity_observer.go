package routes

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
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
	// identityLogKeysMax bounds the once-per-interval log memory (#3999, the #3994 audit): a
	// caller that changes a value the key is built from must not grow the map without end.
	identityLogKeysMax = 1024
	identityRecentMax  = 50
	// identityBreakKeysMax bounds the would-break breakdown (distinct caller, tool and
	// route triples); later new triples are counted under "other". One agent calling every guarded route (TestGuardMatchesEveryTableEntry) must fit: the tables passed 200 entries with #5572.
	identityBreakKeysMax = 400
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
	// actorClaims counts board requests by "actor-kind outcome METHOD /route" (#4244): how the
	// names a request gives for itself compared with the actor the identity layer decided.
	actorClaims map[string]int64
	// joiningWould counts requests from a token with no member name yet (joining, or on no board) to a
	// board route other than subscribe, by "METHOD /route": what the joining rule would refuse (#4255).
	joiningWould map[string]int64
	recent       []IdentityMismatch
	lastLog      map[string]time.Time
	now          func() time.Time
}

// NewIdentityObserver returns the observer; hasSession reports a valid dashboard cookie.
func NewIdentityObserver(db *store.DB, bs *board.Store, hasSession func(*http.Request) bool) *IdentityObserver {
	return &IdentityObserver{db: db, bs: bs, hasSession: hasSession,
		callers: map[string]int64{}, bySession: map[string]int64{}, byTool: map[string]int64{},
		mismatches: map[string]int64{}, breakBy: map[string]int64{}, refused: map[string]int64{}, actorClaims: map[string]int64{}, joiningWould: map[string]int64{}, operatorCalls: map[string]int64{}, lastLog: map[string]time.Time{}, now: time.Now}
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

// effectiveStrict is strict local auth OR require_auth_on_loopback (#3585): the setting makes
// every direct-local request strict-local, so a tokenless one is refused and a token, session or
// CLI signature is verified here, exactly as strict mode does. A proxied request never reaches
// this middleware with the loopback pass, so it is unaffected.
func (o *IdentityObserver) effectiveStrict() bool {
	return o != nil && (o.strict || auth.RequireAuthOnLoopback())
}

// strictFor is effectiveStrict for one request: also strict when its loopback connection isn't
// Coral's own OS user's (auth.PeerReason, #95): another user of this computer always needs a
// credential, whatever the settings.
func (o *IdentityObserver) strictFor(r *http.Request) bool {
	return o != nil && (o.effectiveStrict() || auth.PeerReason(r) != "")
}

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
			if o.strictFor(r) && o.lockPage(w, r) {
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// Every board request's body is capped before anything reads it: the identity check below
		// (boardClaims) for an agent's token, and the handler's decoder for every caller, token or
		// not (#3668 finding 5, #3695). A declared length over the cap is refused here with 413; a
		// chunked body is cut at the cap, so its handler fails to decode it.
		// The relayed file route is the exception (#4453): its body is a file frame, with its own bounds
		// (bodyLimits, and the handler's checks on the declared length before a byte is read).
		if r.Body != nil && strings.HasPrefix(r.URL.Path, "/api/board/") && !isRelayedFileRoute(r) {
			if r.ContentLength > maxBoardBody {
				o.refuse(w, http.StatusRequestEntityTooLarge, "body_too_large", "This board request's body is over the limit")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBoardBody)
		}
		if token := r.Header.Get(store.SessionTokenHeader); token != "" {
			owner, err := o.db.SessionTokenOwnerOf(r.Context(), token)
			if err != nil {
				o.count(callerAgentBadToken)
				o.logOnce("bad_token", "[identity] request with an unknown or revoked session token", "request", r.Method+" "+r.URL.Path, "tool", r.UserAgent(), "strict", o.strict)
				if o.strictFor(r) {
					o.countBreak(callerAgentBadToken, r)
					o.refuse(w, http.StatusUnauthorized, "session_token_required",
						"This agent's session token is unknown or no longer valid (the agent was restarted or closed). Restart the agent from Coral.")
					return
				}
				o.serveAs(w, r, next, callerAgentBadToken)
				o.countBreak(callerAgentBadToken, r)
				return
			}
			o.count(callerAgentToken)
			o.attribute(owner.SessionName, r.UserAgent())
			if o.strictFor(r) {
				// The token is the whole identity: a command-line signature on the same
				// request never adds the Operator's authority (#3418).
				r.Header.Del(cliauth.CLIAuthHeader)
			}
			r = r.WithContext(context.WithValue(r.Context(), verifiedSessionKey{}, owner))
			if route, only, ok := guardedRoute(r); ok {
				o.countOperatorCall(owner.SessionName, r.Method+" "+route)
				// In every mode (#3929): an agent's token is who the caller is, as for another
				// board (#3869). A request with no token is left to strict mode.
				if only {
					o.refuse(w, http.StatusForbidden, "forbidden_for_agent",
						"An agent can't "+operatorOnlyRoutes[r.Method+" "+route]+": that is for the Operator"+operatorOnlyWhere(r.Method+" "+route))
					return
				}
			}
			// One session's data is that session's, and its board's (#5013): an agent reads itself and the sessions on its own board.
			if key, scoped := scopedSessionKey(r); scoped {
				may, err := o.agentMaySeeSession(r.Context(), r, owner, key)
				if err != nil {
					o.logOnce("session-scope:"+owner.SessionName, "[identity] session lookup failed, request refused", "session", owner.SessionName, "request", r.Method+" "+r.URL.Path, "error", err)
					o.refuse(w, http.StatusServiceUnavailable, "identity_unavailable", "Coral couldn't check which sessions this agent may read just now; try again")
					return
				}
				if !may {
					o.refuse(w, http.StatusForbidden, "forbidden_other_session",
						"An agent can read its own session and the sessions on its own board, not another's: that is for the Operator (the dashboard, or the command line outside an agent)")
					return
				}
			}
			// A presented token decides who the caller is on the board, in every mode
			// (#3495): an agent can't post or act as another subscriber or as the Operator,
			// nor act on a board its session isn't subscribed on (#3869: an agent read other
			// teams' boards by name), apart from joining one and a cross-team send.
			m, actorName, err := o.checkBoard(r, owner)
			if errors.Is(err, errBoardBodyTooLarge) {
				o.refuse(w, http.StatusRequestEntityTooLarge, "body_too_large", "This board request's body is over the limit")
				return
			}
			if err != nil {
				// The board's subscribers couldn't be read, so a name the request claims can't be
				// told apart from one another session holds: refused, not let through (#3512).
				o.logOnce("board-lookup:"+owner.SessionName, "[identity] board lookup failed, request refused",
					"session", owner.SessionName, "request", r.Method+" "+r.URL.Path, "error", err)
				o.refuse(w, http.StatusServiceUnavailable, "identity_unavailable",
					"Coral couldn't check this agent's board identity just now; try again")
				return
			}
			if m != nil {
				o.refuse(w, http.StatusForbidden, "identity_mismatch",
					"This agent can't act as "+strconv.Quote(m.Claimed)+" ("+m.Field+"): its session is "+strconv.Quote(m.Expected)+" ("+m.Kind+")")
				return
			}
			// Who the request is from, decided here and read from here (#4244): the member name the
			// token's session holds on this board. The claim is compared in log mode.
			actor := Actor{Kind: ActorToken, Name: actorName, Session: owner.SessionName}
			r = withActor(r, actor)
			var given []string
			var readErr error
			isBoard := strings.HasPrefix(r.URL.Path, "/api/board/")
			if isBoard {
				given, readErr = attributionClaims(r)
			}
			next.ServeHTTP(w, r)
			if isBoard {
				o.countActor(r, actor, given, readErr)
			}
			return
		}
		caller := o.classify(r)
		o.count(caller)
		if caller == callerAgentNoToken {
			if route, _, ok := guardedRoute(r); ok {
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
		if o.strictFor(r) || claimed {
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
						"The command-line signature is not valid for this Coral: another data folder's token, a used or expired signature, or a coral command from another version (since v1.4.70 the signature covers the query too). Use the coral command that came with this Coral.")
				case callerAPIKey, callerHub:
					o.refuse(w, http.StatusUnauthorized, "invalid_credential",
						"The credential sent is not valid for this Coral (a wrong API key, or a Coral Hub stamp that isn't this connector's): it is refused, never ignored")
				default:
					if reason := auth.PeerReason(r); reason != "" {
						// Another account on this computer (or one Coral couldn't attribute): the
						// loopback pass is only for Coral's own account (#95). The account isn't named.
						o.refuseDetail(w, http.StatusUnauthorized, "other_user_needs_sign_in", reason,
							"This request comes from another account on this computer, or one Coral couldn't identify. Sign in with Coral's password, or send the API key (Authorization: Bearer) or an agent's session token: the no-sign-in pass on this computer is only for Coral's own account.")
						break
					}
					o.refuse(w, http.StatusUnauthorized, "dashboard_auth_required",
						"Coral is locked: open it from the Coral app, or run 'coral open' to sign in this browser. Agents: use coral-agent docs, or send X-Coral-Session-Token")
				}
				return
			}
			o.serveAs(w, r2, next, caller)
			return
		}
		o.serveAs(w, r, next, caller)
		if wouldBreak[caller] {
			o.countBreak(caller, r)
		}
	})
}

// admit applies strict local auth to a request with no session token: the request (with
// what was verified recorded on it) and whether it may go on.
func (o *IdentityObserver) admit(r *http.Request, caller string) (*http.Request, bool) {
	if strictOpenPaths[r.Method+" "+routedPath(r)] {
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
	p := routedPath(r)
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

// refuseDetail is refuse with a short detail code (#95: other_user or unattributed).
func (o *IdentityObserver) refuseDetail(w http.ResponseWriter, status int, code, detail, msg string) {
	o.mu.Lock()
	o.refused[code]++
	o.mu.Unlock()
	writeJSON(w, status, map[string]any{"error": msg, "code": code, "detail": detail})
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

// operatorOrKeyCaller reports whether the request is the Operator's: a person (humanInputCaller), or the server's own
// API key, which is how a hub reaches a registered server (pkg/hubproxy sends "Authorization: Bearer <key>" and nothing of the browser) and which the identity
// layer already treats as the Operator (actorFor). The identity middleware refuses a key that is not valid before any handler runs, so a token-less request of
// the api_key kind has one. An agent's session token is never the Operator, whatever else the request carries. Used where a person's own data is at stake and the
// hub must be able to act for them (the saved messages, #4551); input to an agent keeps the stricter humanInputCaller.
func operatorOrKeyCaller(r *http.Request) bool {
	if humanInputCaller(r) {
		return true
	}
	if r.Header.Get(store.SessionTokenHeader) != "" {
		return false
	}
	if _, ok := r.Context().Value(verifiedSessionKey{}).(*store.SessionTokenOwner); ok {
		return false
	}
	kind, _ := r.Context().Value(callerKindKey{}).(string)
	return kind == callerAPIKey
}

// classify names the caller of a request that carries no session token.
func (o *IdentityObserver) classify(r *http.Request) string {
	switch {
	case strictOpenPaths[r.Method+" "+routedPath(r)]:
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
	route := routeOf(r)
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
	if len(tool) > 40 {
		tool = tool[:40]
	}
	o.mu.Lock()
	if _, ok := o.byTool[tool]; !ok && len(o.byTool) >= identityBreakKeysMax {
		tool = "other" // a token holder that varies its User-Agent can't grow the map (#3999)
	}
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
	if !seen && len(o.lastLog) >= identityLogKeysMax {
		for k, t := range o.lastLog { // forget what is due again anyway
			if now.Sub(t) >= identityLogInterval {
				delete(o.lastLog, k)
			}
		}
		if len(o.lastLog) >= identityLogKeysMax {
			o.mu.Unlock() // a flood of distinct keys inside one interval: skip the line, not the memory
			return
		}
	}
	o.lastLog[key] = now
	o.mu.Unlock()
	slog.Warn(msg, args...)
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
	msg := "[identity] mismatch refused"
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
	actorClaims := map[string]int64{}
	for k, v := range o.actorClaims {
		actorClaims[k] = v
	}
	joining := map[string]int64{}
	for k, v := range o.joiningWould {
		joining[k] = v
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
	if o.effectiveStrict() {
		mode = "strict"
	}
	return map[string]any{"mode": mode, "callers": callers, "would_break_in_strict_mode": breaking, "would_break_by_route": breakBy, "refused": refused, "agent_operator_calls": opCalls, "actor_claims": actorClaims, "joining_would_refuse": joining, "enforce_would_differ": enforceDiffersSnapshot(), "identity_actor": actorMode(),
		"mismatches": mm, "recent_mismatches": recent, "attributed_by_session": sessions, "attributed_by_tool": tools}
}

// resolvedBoardKey is the board id a /api/board/<key> request acts on: the key itself when
// it is an id or names no single board, else the id of the one board labelled key (the
// same rule as BoardKeyResolver).
func resolvedBoardKey(r *http.Request, bs *board.Store, key string) string {
	if bs == nil || !board.UsableBoardKey(key) {
		return key
	}
	if id, _, err := resolveBoard(r, bs, key); err == nil {
		return id
	}
	return key
}

// AgentOperatorAction is for an Operator-only choice a handler makes from the request body
// (#3536: new_board for a name that's taken). For an agent's token it records the call in
// agent_operator_calls and, with strict local auth, returns true: the handler refuses it
// (403 forbidden_for_agent). The Operator, and callers without a token, get false.
func (o *IdentityObserver) AgentOperatorAction(r *http.Request, action string) bool {
	owner, ok := VerifiedSession(r.Context())
	if o == nil || !ok || owner == nil {
		return false
	}
	o.countOperatorCall(owner.SessionName, action)
	return o.strictFor(r)
}

// acrossBoards: the requests an agent may make on a board its session isn't subscribed on
// (#3869): joining it, and a cross-team send to it (whose handler checks that the sender is
// an active Orchestrator of the board it sends from, in its own session).
func acrossBoards(method, tail string) bool {
	return method == http.MethodPost && (tail == "subscribe" || tail == "external-messages")
}
