// Who is the machine's owner behind a Coral Hub request (#5280, #5307): the checks of section 13, shared by the update routes and the owner's
// configuration routes (a board's health check setup, the prompt inspection).

package routes

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// callerIdentity is who the stamp says is asking: owner is true only when every owner check passes.
type callerIdentity struct {
	owner  bool
	pseudo string // the stamped hub user when it has the stamp's form; "" otherwise
	// local is the owner at this computer's own dashboard or command line (a request that did not come through the Hub's tunnel): no Hub gate applies to it, its pseudonym is "local".
	local bool
}

// hubOwnerLabel is the owner's hub-user label recorded at enrolment, read from the enrolment record. The server sets it at startup
// (SetHubOwnerLabel); unset, a request has no record and the hub's marker alone decides (as for a machine enrolled before the label).
var (
	hubOwnerLabel      func(ctx context.Context) (string, error)
	hubOwnerNoLabelLog sync.Once
)

// SetHubOwnerLabel sets where the routes read the owner's enrolled label.
func SetHubOwnerLabel(f func(ctx context.Context) (string, error)) { hubOwnerLabel = f }

// hubOwnerRequest is THE owner check (#5610): who the stamp says is asking, with the server's enrolment record. Whether the enrolled owner label is REQUIRED is the route table's
// (tunnel.OwnerMode.LabelRequired): the owner routes need it (a machine whose enrolment recorded no owner label, linked before it was recorded, refuses them until it is linked again: the
// hub's marker alone is not enough to read or change the configuration or the artifacts), the update routes accept the marker alone (#5280). A route that is not in the table needs it.
func hubOwnerRequest(r *http.Request) callerIdentity {
	return ownerIdentityWith(r, hubOwnerLabel, &hubOwnerNoLabelLog)
}

// ownerIdentityWith is hubOwnerRequest with another source of the enrolled label (the update handler has its own, set at wiring). It is the only caller of identifyOwner.
func ownerIdentityWith(r *http.Request, label func(context.Context) (string, error), noLabelLogged *sync.Once) callerIdentity {
	id := identifyOwner(r, label, noLabelLogged)
	if id.owner && routeNeedsOwnerLabel(r) && !ownerLabelRecordedWith(r, label) {
		id.owner = false
	}
	return id
}

// routeNeedsOwnerLabel is the table's answer for the request's route; a route the table does not list needs the label.
func routeNeedsOwnerLabel(r *http.Request) bool {
	_, mode := tunnel.OwnerRoute(r.Method, r.URL.Path)
	return mode == tunnel.OwnerNone || mode.LabelRequired()
}

func ownerLabelRecordedWith(r *http.Request, label func(context.Context) (string, error)) bool {
	if label == nil {
		return false
	}
	l, err := label(r.Context())
	return err == nil && l != ""
}

// ownerRefusals are the words a non-owner gets (403, code owner_only) on each route the table marks owner-only, by "METHOD pattern". The decision is the table's and one helper's
// (refuseNonOwner); only the sentence belongs to the route.
var ownerRefusals = map[string]string{
	tunnel.OwnerRouteKey("GET", "/api/artifacts/{hex64}"):                           textArtifactsOwnerOnly,
	tunnel.OwnerRouteKey("GET", "/api/artifacts"):                                   textArtifactsOwnerOnly,
	tunnel.OwnerRouteKey("GET", "/api/artifacts/{hex64}/meta"):                      textArtifactsOwnerOnly,
	tunnel.OwnerRouteKey("GET", "/api/artifacts/{hex64}/comments"):                  textArtifactsOwnerOnly,
	tunnel.OwnerRouteKey("GET", "/api/artifacts/{hex64}/readers"):                   textArtifactsOwnerOnly,
	tunnel.OwnerRouteKey("GET", "/api/board/{project}/tasks/{id}/artifact-content"): textArtifactsOwnerOnly,
	tunnel.OwnerRouteKey("GET", "/api/board/{project}/artifacts"):                   textArtifactsOwnerOnly,
	tunnel.OwnerRouteKey("GET", "/api/session-artifacts"):                           textArtifactsOwnerOnly,
	tunnel.OwnerRouteKey("DELETE", "/api/artifacts/{hex64}"):                        "through Coral Hub only the account this computer is linked to can delete an artifact",
	tunnel.OwnerRouteKey("POST", "/api/artifacts/{hex64}/comments"):                 textCommentsOwnerOnly,
	tunnel.OwnerRouteKey("POST", "/api/artifacts/{hex64}/comments/{id}/resolve"):    textCommentsOwnerOnly,
	tunnel.OwnerRouteKey("POST", "/api/artifacts/{hex64}/comments/{id}/reopen"):     textCommentsOwnerOnly,
	tunnel.OwnerRouteKey("PUT", "/api/board/{project}/settings"):                    "only the account this computer is linked to can change board settings through Coral Hub",
	tunnel.OwnerRouteKey("GET", tunnel.PromptInspectionPath):                        "only the account this computer is linked to can read the prompts through Coral Hub",
	tunnel.OwnerRouteKey("GET", "/api/board/{project}/cross-team/settings"):         textHubSwitchOwnerOnly,
	tunnel.OwnerRouteKey("GET", "/api/custom-models"):                               textCustomModelsOwnerOnly,
	tunnel.OwnerRouteKey("PUT", "/api/custom-models"):                               textCustomModelsOwnerOnly,
	tunnel.OwnerRouteKey("GET", "/api/saved"):                                       textSavedOwnerOnly,
	tunnel.OwnerRouteKey("GET", "/api/saved/{id}/message"):                          textSavedOwnerOnly,
	tunnel.OwnerRouteKey("POST", "/api/saved"):                                      textSavedOwnerOnly,
	tunnel.OwnerRouteKey("PATCH", "/api/saved/{id}"):                                textSavedOwnerOnly,
	tunnel.OwnerRouteKey("DELETE", "/api/saved/{id}"):                               textSavedOwnerOnly,
	tunnel.OwnerRouteKey("POST", "/api/board/{project}/cross-team/enable"):          textHubSwitchOwnerOnly,
	tunnel.OwnerRouteKey("POST", "/api/board/{project}/cross-team/disable"):         textHubSwitchOwnerOnly,
	tunnel.OwnerRouteKey("POST", "/api/board/{project}/cross-team/files/enable"):    textHubSwitchOwnerOnly,
	tunnel.OwnerRouteKey("POST", "/api/board/{project}/cross-team/files/disable"):   textHubSwitchOwnerOnly,
	tunnel.OwnerRouteKey("POST", "/api/hub-auto-accept"):                            textHubAutoAcceptOwnerOnly,
}

const textCustomModelsOwnerOnly = "through Coral Hub only the account this computer is linked to can add or change custom models (#5996)"

const textSavedOwnerOnly = "through Coral Hub the saved messages are for the account this computer is linked to only (#5753)"

const textHubAutoAcceptOwnerOnly = "through Coral Hub only the account this computer is linked to can turn Coral Hub auto-accept on or off; on this computer use Settings > Network and Hub"

const textCommentsOwnerOnly = "through Coral Hub only the account this computer is linked to can comment on artifacts (#7052)"

const textArtifactsOwnerOnly = "through Coral Hub artifacts and pasted images are for the account this computer is linked to only"

// refuseNonOwner answers 403 owner_only, in the route's words, to a request that came through Coral Hub on a route the table marks owner-only from anyone but the owner, and says it did.
// Every owner-only handler calls it first; a request from this computer is refused only on the routes that are the Hub owner's alone (OwnerHubOnly). A route that is not in the table, or has no sentence, is refused in general words:
// the table is the truth and the check fails closed.
func refuseNonOwner(w http.ResponseWriter, r *http.Request) bool {
	pattern, mode := tunnel.OwnerRoute(r.Method, r.URL.Path)
	if !cloud.Tunneled(r) && !mode.RefusesLocal() {
		return false
	}
	if mode != tunnel.OwnerNone && !mode.Refuses() {
		return false // the handler decides what a non-owner gets on this route
	}
	if hubOwnerRequest(r).owner {
		return false
	}
	if ownerLabelMissingFor(r) {
		if adoptOwnerFromRequest(r) {
			return false // the person pressed Confirm owner a moment ago: this request is the one that was waited for, and it is served as the owner's (#6108)
		}
		// The hub says this is the owner, but this computer recorded no owner at enrolment (it was linked before Coral recorded one): say that, not "you are not the owner" (#6108).
		writeJSON(w, http.StatusForbidden, map[string]string{"code": ownerLabelMissingCode, "error": textOwnerLabelMissing})
		return true
	}
	text, ok := ownerRefusals[tunnel.OwnerRouteKey(r.Method, pattern)]
	if !ok {
		text = "through Coral Hub this is for the account this computer is linked to only"
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"code": "owner_only", "error": text})
	return true
}

// adoptOwnerFromRequest is called by refuseNonOwner for a request that passed every owner check except the missing label (ownerLabelMissingFor): an owner-only route, the connector's own
// stamp, the owner marker and a hub user of the exact form. If the adoption is armed it is used up here, and when the connected hub is the enrolled one the request's hub user becomes the owner
// label; true means the label was recorded and the request goes on as the owner's. Only GET and HEAD adopt, and never while several hub accounts were stamped as the owner in the last hour.
func adoptOwnerFromRequest(r *http.Request) bool {
	if hubOwnerAdopt == nil {
		return false
	}
	// Only a read can carry the adoption (a write is refused without using the arm up): a label recorded from a request that changes something would let that request act before anyone confirmed the account.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	id := identifyOwner(r, hubOwnerLabel, &hubOwnerNoLabelLog)
	if !id.owner || !tunnel.ValidHubUser(id.pseudo) {
		return false
	}
	// More than one hub account was stamped as the owner in the last hour: there is no one account to trust, so nothing is adopted (and the arm stays for the person to see the answer).
	if _, _, several := ownerCandidate(ownerNow()); several {
		return false
	}
	if !takeOwnerArm(ownerNow()) {
		return false
	}
	return hubOwnerAdopt(r.Context(), id.pseudo)
}

// ownerLabelMissingCode is the refusal code of an owner-marked request to a machine that recorded no owner label (#6108); the dashboard says what to do about it.
const ownerLabelMissingCode = "owner_label_missing"

const textOwnerLabelMissing = "Coral Hub can't confirm you own this computer yet. On the computer, open Settings > Network and Hub and choose Confirm owner."

// ownerLabelMissingFor reports whether r is a stamped owner request refused only because no owner label is recorded: the stamp passes every owner check (identifyOwner), the label was read
// without an error and is empty. An unreadable record is not "missing": that stays a plain refusal.
func ownerLabelMissingFor(r *http.Request) bool {
	if hubOwnerLabel == nil {
		return false
	}
	if !identifyOwner(r, hubOwnerLabel, &hubOwnerNoLabelLog).owner {
		return false
	}
	l, err := hubOwnerLabel(r.Context())
	return err == nil && l == ""
}

// ownerStamps remembers which hub users the Hub stamped as the owner on authentic requests in the last hour, so the person at this computer can be shown who it would record (#6108): never
// written to disk, and only the stamp's own form (hub-user-<8 hex>) is kept.
var ownerStamps = struct {
	sync.Mutex
	seen map[string]time.Time
}{seen: map[string]time.Time{}}

const (
	ownerStampWindow = time.Hour
	ownerStampsMax   = 4
)

func noteOwnerStamp(user string, now time.Time) {
	ownerStamps.Lock()
	defer ownerStamps.Unlock()
	for u, at := range ownerStamps.seen {
		if now.Sub(at) >= ownerStampWindow {
			delete(ownerStamps.seen, u)
		}
	}
	if _, ok := ownerStamps.seen[user]; !ok && len(ownerStamps.seen) >= ownerStampsMax {
		return // bounded: a hub that stamps many different owners is itself the answer, and no candidate is offered
	}
	ownerStamps.seen[user] = now
}

// ownerCandidate is the one hub user stamped as the owner in the last hour; several is true when more than one was, and then none is offered.
func ownerCandidate(now time.Time) (user string, at time.Time, several bool) {
	ownerStamps.Lock()
	defer ownerStamps.Unlock()
	for u, t := range ownerStamps.seen {
		if now.Sub(t) >= ownerStampWindow {
			continue
		}
		if user != "" {
			return "", time.Time{}, true
		}
		user, at = u, t
	}
	return user, at, false
}

// ownerSeveralClearsIn is how long until at most one account is left among the stamps of the last hour: the time until the second-newest stamp ages out of the window (a newer stamp from another account
// before then puts it off again). 0 when there are not several now.
func ownerSeveralClearsIn(now time.Time) time.Duration {
	ownerStamps.Lock()
	defer ownerStamps.Unlock()
	var live []time.Time
	for _, t := range ownerStamps.seen {
		if now.Sub(t) < ownerStampWindow {
			live = append(live, t)
		}
	}
	if len(live) < 2 {
		return 0
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Before(live[j]) })
	return live[len(live)-2].Add(ownerStampWindow).Sub(now)
}

// identifyOwner runs the stamp and owner checks. It never trusts a header unless the request really came through the connector: the stamp must
// be the connector's own, the owner marker must be there once and be "owner", the hub user must have the stamp's form, and when the enrolment
// recorded the owner's label the user must equal it. An unreadable record is no owner.
func identifyOwner(r *http.Request, ownerUser func(context.Context) (string, error), noLabelLogged *sync.Once) callerIdentity {
	var id callerIdentity
	if !cloud.TunneledAuthentic(r) {
		return id
	}
	if u := r.Header.Get(tunnel.HubUserHeader); tunnel.ValidHubUser(u) && len(r.Header.Values(tunnel.HubUserHeader)) == 1 {
		id.pseudo = u
	}
	roles := r.Header.Values(tunnel.HubRoleHeader)
	if len(roles) != 1 || roles[0] != tunnel.HubRoleOwner {
		return id
	}
	if id.pseudo == "" {
		return id
	}
	noteOwnerStamp(id.pseudo, ownerNow())
	var label string
	if ownerUser != nil {
		l, err := ownerUser(r.Context())
		if err != nil {
			return id // fail closed: an unreadable record is no owner
		}
		label = l
	}
	if label == "" {
		// Enrolled before the label was recorded: the hub's marker alone (never recorded from a request).
		noLabelLogged.Do(func() {
			slog.Warn("[hub] this enrolment recorded no owner label: the hub's owner marker is the only owner check; link this computer again to record it")
		})
		id.owner = true
		return id
	}
	id.owner = subtle.ConstantTimeCompare([]byte(label), []byte(id.pseudo)) == 1
	return id
}
