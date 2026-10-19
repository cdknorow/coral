package routes

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	setreg "github.com/cdknorow/coral/internal/settings"
	"github.com/cdknorow/coral/internal/store"
)

// identity_actor (#4255, identity phase 2): "log" (the default) leaves every decision as it was and
// counts, per site and route, where deciding by the actor alone would decide differently; "enforce"
// decides by the actor alone (the member name or the Operator the identity layer decided, never what
// the request claims, and nothing privileged accepts a claimed actor). It is turned on only after the
// identity harness has run clean for a release and the Operator says so; the switch is the Operator's
// (set only through the Operator-only setting, never by an agent, never through Coral Hub).
var actorEnforced atomic.Bool

func identityActorEnforced() bool { return actorEnforced.Load() }

// setIdentityActorEnforced is the one place the mode changes (the Operator's setting, and tests).
func setIdentityActorEnforced(on bool) { actorEnforced.Store(on) }

// enforceDiffers counts decisions that enforce would make differently from log mode, by "site
// METHOD /route" (a site with no request, such as a background rule, counts under its own key). It is
// process-wide, like the other identity counters, and bounded.
var enforceDiffers = struct {
	mu sync.Mutex
	m  map[string]int64
}{m: map[string]int64{}}

func noteEnforceDiffersKey(key string) {
	enforceDiffers.mu.Lock()
	defer enforceDiffers.mu.Unlock()
	if _, ok := enforceDiffers.m[key]; !ok && len(enforceDiffers.m) >= identityBreakKeysMax {
		key = "other"
	}
	enforceDiffers.m[key]++
}

// noteEnforceDiffers records, in log mode, that enforce would have decided request r differently at site.
func noteEnforceDiffers(site string, r *http.Request) {
	noteEnforceDiffersKey(site + " " + r.Method + " " + routeOf(r))
}

// enforceDiffersSnapshot is the counters, for the identity report.
func enforceDiffersSnapshot() map[string]int64 {
	enforceDiffers.mu.Lock()
	defer enforceDiffers.mu.Unlock()
	out := make(map[string]int64, len(enforceDiffers.m))
	for k, v := range enforceDiffers.m {
		out[k] = v
	}
	return out
}

// enforceDiffersSites is the sorted keys of the counters (tests).
func enforceDiffersSites() []string {
	snap := enforceDiffersSnapshot()
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// resetEnforceDiffers clears the counters (tests).
func resetEnforceDiffers() {
	enforceDiffers.mu.Lock()
	enforceDiffers.m = map[string]int64{}
	enforceDiffers.mu.Unlock()
}

// actorMode is the mode's name, for the identity report.
func actorMode() string {
	if identityActorEnforced() {
		return "enforce"
	}
	return "log"
}

// actorName is the name an actor attributes a request to, and whether it has one: the board member a
// token holds, "Operator" for the verified Operator, the name the tunnel decided for a Hub request.
// A claimed actor, and a token with no member name yet, have none.
func actorName(a Actor) (string, bool) {
	switch {
	case a.Kind == ActorToken && a.Name != "":
		return a.Name, true
	case a.Kind == ActorOperator:
		return "Operator", true
	case a.Kind == ActorHub && a.Name != "":
		return a.Name, true
	}
	return "", false
}

// attributedTo is the name a handler attributes request r to (#4255): in log mode the name the request
// claims, as before, counted when the actor alone would refuse it or name someone else; under enforce
// the actor's name, the claim being optional, and a request with no verified actor refused with 403.
// A handler assigns the result back to the field it read the claim from, so the rest of it reads the
// attributed name.
func attributedTo(w http.ResponseWriter, r *http.Request, claim string) (string, bool) {
	if callerActor(r).Kind == ActorHub {
		return claim, true // through Coral Hub the tunnel's guard has decided who may be named (TunnelIdentityGuard)
	}
	name, verified := actorName(callerActor(r))
	if identityActorEnforced() {
		if !verified {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "this request is not from a verified member of the board or the Operator", "code": "identity_unverified"})
			return "", false
		}
		return name, true
	}
	if c := strings.TrimSpace(claim); !verified || (c != "" && !board.SameIdentity(c, name)) {
		noteEnforceDiffers("attribution", r)
	}
	return claim, true
}

// attributedSubject is the name a handler acts on when the request's subscriber_id names the subject of
// the action, not the caller (leaving a board, a reminder): a member's token acts on itself, the Operator
// (the dashboard, the signed command line) may name any member, and under enforce a request with no
// verified actor is refused. In log mode it is the claim, counted where enforce would decide otherwise.
func attributedSubject(w http.ResponseWriter, r *http.Request, claim string) (string, bool) {
	a := callerActor(r)
	if name, member := a.Member(); member {
		if identityActorEnforced() {
			return name, true
		}
		if c := strings.TrimSpace(claim); c != "" && !board.SameIdentity(c, name) {
			noteEnforceDiffers("attribution", r)
		}
		return claim, true
	}
	verified := a.Kind == ActorOperator || (a.Kind == ActorHub && a.Name != "")
	if identityActorEnforced() && !verified {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this request is not from a verified member of the board or the Operator", "code": "identity_unverified"})
		return "", false
	}
	if !verified {
		noteEnforceDiffers("attribution", r)
	}
	return claim, true
}

// attributedJoin is the name and session a subscribe is made with. A token joins under the name it
// chooses (it has none yet) from its OWN session, whatever session it names; a member re-subscribing is
// that member; the Operator names what it joins as; under enforce a request with no verified actor is
// refused. In log mode it is the claim, counted where enforce would decide otherwise.
func attributedJoin(w http.ResponseWriter, r *http.Request, claimName, claimSession string) (string, string, bool) {
	a := callerActor(r)
	hubUser := a.Kind == ActorHub && a.Name != "" // the tunnel's guard named the Hub user
	verified := a.Kind == ActorToken || a.Kind == ActorOperator || hubUser
	if !identityActorEnforced() {
		if !verified || (a.Kind == ActorToken && ((a.Name != "" && strings.TrimSpace(claimName) != "" && !board.SameIdentity(strings.TrimSpace(claimName), a.Name)) ||
			(strings.TrimSpace(claimSession) != "" && claimSession != a.Session))) {
			noteEnforceDiffers("attribution", r)
		}
		return claimName, claimSession, true
	}
	switch {
	case a.Kind == ActorToken:
		name := claimName
		if a.Name != "" {
			name = a.Name
		}
		return name, a.Session, true
	case a.Kind == ActorOperator:
		return claimName, claimSession, true
	case hubUser:
		// Reached only if the tunneled branch above is bypassed; a Hub actor should then pass an
		// empty session.
		// A Hub user joins as the name the tunnel decided, never as the Operator (unless it is the
		// Hub operator) or as another member: the claim is ignored, and a claim of the Operator's
		// name by anyone else is refused, as attributedTo's guard would.
		if c := strings.TrimSpace(claimName); c != "" && board.SameIdentity(c, "Operator") && !a.IsOperator() {
			break
		}
		return a.Name, claimSession, true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "this request is not from a verified member of the board or the Operator", "code": "identity_unverified"})
	return "", "", false
}

// IdentityActorSetting stores the identity_actor mode ("log" or "enforce"). Only SetIdentityActor
// writes it (PutSettings refuses the key), so the Operator's decision is the only way it changes.
const IdentityActorSetting = "identity_actor"

// LoadIdentityActor applies the stored mode at startup. Anything but "enforce" is log, the default.
func LoadIdentityActor(ctx context.Context, ss *store.SessionStore) {
	settings, err := ss.GetSettings(ctx)
	setIdentityActorEnforced(err == nil && setreg.Enum(settings, IdentityActorSetting) == "enforce")
}

// SetIdentityActor: PUT /api/system/identity-actor {"mode": "log"|"enforce"}. The Operator's switch:
// refused through Coral Hub, and refused here for anything but the verified Operator (an agent's
// token, a claimed caller), so the check does not rest on the route list alone.
func (h *CloudHandler) SetIdentityActor(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) {
		errForbidden(w, "The identity mode is only available on this computer")
		return
	}
	if a := callerActor(r); a.Kind != ActorOperator || !a.IsOperator() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the Operator may change the identity mode", "code": "operator_only"})
		return
	}
	if !requireJSON(w, r) {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &body); err != nil || (body.Mode != "log" && body.Mode != "enforce") {
		errBadRequest(w, "mode must be \"log\" or \"enforce\"")
		return
	}
	if err := h.ss.SetSetting(r.Context(), IdentityActorSetting, body.Mode); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	setIdentityActorEnforced(body.Mode == "enforce")
	writeJSON(w, http.StatusOK, map[string]string{"identity_actor": actorMode()})
}
