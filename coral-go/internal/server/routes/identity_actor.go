package routes

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// Actor is who a request is from, as the identity layer decided it (#4244, identity phase 2 of
// #4232). It is the single source for attribution: handlers will take the name they attribute a
// request to from here instead of decoding subscriber_id again (the next steps move them over, in
// groups); until then it is set and compared with each request's claim in log mode, and nothing
// changes for the request.
//
// Only the identity layer sets it, in a context value no client can reach (the key type is
// unexported, and a header is never read): a request cannot bring its own.
type Actor struct {
	// Kind says what backs the name:
	//   ActorToken    an agent's session token: Name is the member name the token's session holds on
	//                 this board ("" for a session that is not on it yet, e.g. one joining);
	//   ActorOperator the Operator: the dashboard's cookie, the signed command line, an API key;
	//   ActorHub      Coral Hub: the name is what the tunnel decides, "Operator" when TunnelIdentityGuard
	//                 admitted the write as the Hub operator (HubUser is then the Hub user acting for
	//                 it), else the Hub user itself (a chat post, a read), or "" when the request names
	//                 no valid Hub user. A chat post is not the guard's: PostMessage decides whether
	//                 one is the Operator's, so there Name is the Hub user whatever the post claims;
	//   ActorClaimed  nothing backs it: a caller with no credential (an install that opted out of
	//                 strict local auth, a script, a bare command line). Name is empty; the request's
	//                 claim is a claim. Nothing privileged may accept it (rule 13, park, ack,
	//                 settings, the Operator name, reserved names).
	Kind string
	// Name is the member name for ActorToken, and "Operator" for ActorOperator.
	Name string
	// Session is the agent session name the token belongs to (ActorToken).
	Session string
	// HubUser is the validated Coral Hub user of an ActorHub request (hub-user-<8 hex>), or "".
	HubUser string
}

// Actor kinds.
const (
	ActorToken    = "token"
	ActorOperator = "operator"
	ActorHub      = "hub"
	ActorClaimed  = "claimed"
)

type actorKey struct{}

// ActorOf is who the request is from, if the identity layer decided it (a request that did not pass
// through it, such as a test calling a handler directly, has none).
func ActorOf(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

func withActor(r *http.Request, a Actor) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), actorKey{}, a))
}

// callerActor is who a handler that attributes a request attributes it to: the identity layer's
// decision. A request that did not pass through the layer (a test calling the handler directly) has
// none, and is claimed: nothing backs a name it gives. Handlers read this, never the claim.
func callerActor(r *http.Request) Actor {
	if a, ok := ActorOf(r.Context()); ok {
		return a
	}
	return Actor{Kind: ActorClaimed}
}

// IsOperator reports whether the actor is the Operator: the dashboard, the signed command line, an
// API key, or the Hub operator the tunnel's guard admitted. Never a token, never claimed.
func (a Actor) IsOperator() bool {
	return a.Name == "Operator" && (a.Kind == ActorOperator || a.Kind == ActorHub)
}

// Member is the board member name a token actor holds on the board, and whether it has one: a token
// with no name is joining (or on no board), and a claimed, Operator or Hub actor is not a member.
func (a Actor) Member() (string, bool) {
	if a.Kind == ActorToken && a.Name != "" {
		return a.Name, true
	}
	return "", false
}

// A token with no member name yet may only join: subscribe is the one board route it may use.
// (Identity phase 2 counts it in log mode; enforce would refuse the rest, not attribute them to "".)
const joiningRoute = "POST /api/board/{project}/subscribe"

// actorFor is the actor of a request that carries no agent token, by how the identity layer
// classified its caller.
func actorFor(r *http.Request, caller string) (Actor, bool) {
	switch caller {
	case callerCLISigned, callerAPIKey, callerDashCookie:
		return Actor{Kind: ActorOperator, Name: "Operator"}, true
	case callerHub:
		return hubActor(r), true
	case callerOpen:
		return Actor{}, false // the liveness check and the command line's hello: nobody acts
	}
	return Actor{Kind: ActorClaimed}, true
}

// Claim outcomes, counted per actor kind and route.
const (
	claimNone      = "no_claim"       // the request names no one
	claimMatch     = "match"          // every name it gives is the actor's
	claimSpelling  = "match_spelling" // the same name in another capitalisation (the signed command line sends "operator")
	claimDiffers   = "differs"        // it names someone else (a token request refused by the identity check never gets here)
	claimUnbound   = "unbound"        // the actor has no member name yet (joining) or the tunnel decides it
	claimUnchecked = "unverified"     // nothing backs the actor: the claim is only a claim
	claimUnread    = "unreadable"     // the body could not be read for its claims
)

// claimOutcome compares the names a board request gives for itself with its actor.
func claimOutcome(a Actor, given []string) string {
	seen, differs, spelling := false, false, false
	for _, v := range given {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		seen = true
		switch {
		case a.Name != "" && v == a.Name:
		case a.Name != "" && strings.EqualFold(v, a.Name):
			spelling = true
		default:
			differs = true
		}
	}
	switch {
	case !seen:
		return claimNone
	case a.Kind == ActorClaimed:
		return claimUnchecked
	case a.Name == "":
		return claimUnbound
	case differs:
		return claimDiffers
	case spelling:
		return claimSpelling
	}
	return claimMatch
}

// attributionClaims are the names a board request gives for itself (the fields handlers attribute
// by), in its query and JSON body.
func attributionClaims(r *http.Request) ([]string, error) {
	claims, err := boardClaims(r)
	if err != nil {
		return nil, err
	}
	return append(append([]string(nil), claims["subscriber_id"]...), claims["created_by"]...), nil
}

// countActor records, once the request has been routed, how its claim compared with its actor
// (log mode: "enforce" would judge on these numbers).
func (o *IdentityObserver) countActor(r *http.Request, a Actor, given []string, readErr error) {
	outcome := claimUnread
	if readErr == nil {
		outcome = claimOutcome(a, given)
	}
	route := routeOf(r)
	key := fmt.Sprintf("%s %s %s %s", a.Kind, outcome, r.Method, route)
	o.mu.Lock()
	defer o.mu.Unlock()
	if a.Kind == ActorToken && a.Name == "" && strings.HasPrefix(route, "/api/board/{project}") && r.Method+" "+route != joiningRoute {
		// A joining token on a route other than subscribe: what enforce would refuse.
		jk := r.Method + " " + route
		if _, ok := o.joiningWould[jk]; !ok && len(o.joiningWould) >= identityBreakKeysMax {
			jk = "other"
		}
		o.joiningWould[jk]++
	}
	if _, ok := o.actorClaims[key]; !ok && len(o.actorClaims) >= identityBreakKeysMax {
		key = a.Kind + " " + outcome + " other"
	}
	o.actorClaims[key]++
}

// routeOf is the route pattern chi matched for r (after routing), or the one it would match.
func routeOf(r *http.Request) string {
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
	return route
}

// serveAs passes a request that carries no agent token on, with its actor set and, for a board
// request, its claim compared with it (log mode).
func (o *IdentityObserver) serveAs(w http.ResponseWriter, r *http.Request, next http.Handler, caller string) {
	actor, ok := actorFor(r, caller)
	if !ok {
		next.ServeHTTP(w, r)
		return
	}
	r = withActor(r, actor)
	isBoard := strings.HasPrefix(r.URL.Path, "/api/board/")
	var given []string
	var readErr error
	if isBoard {
		given, readErr = attributionClaims(r)
	}
	next.ServeHTTP(w, r)
	if isBoard {
		o.countActor(r, actor, given, readErr)
	}
}

// hubActor is the actor of a request that came through Coral Hub: what the tunnel's guard decided
// (it runs before the identity layer). The Hub user is read only from the header the connector
// stamps, and only when the request carries the connector's own stamp (a forged one is refused
// before this): exactly one value, in the form the guard requires.
func hubActor(r *http.Request) Actor {
	a := Actor{Kind: ActorHub}
	if cloud.TunneledAuthentic(r) {
		if values := r.Header[http.CanonicalHeaderKey(tunnel.HubUserHeader)]; len(values) == 1 && len(values[0]) <= 32 && hubUserRe.MatchString(values[0]) {
			a.HubUser = values[0]
			a.Name = values[0]
		}
	}
	if op := hubOperatorUser(r); op != "" { // the guard admitted this write as the Hub operator
		a.Name, a.HubUser = "Operator", op
	}
	return a
}
