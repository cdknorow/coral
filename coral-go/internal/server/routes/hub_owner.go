// Who is the machine's owner behind a Coral Hub request (#5280, #5307): the checks of section 13, shared by the update routes and the owner's
// configuration routes (a board's health check setup, the prompt inspection).

package routes

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"sync"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// callerIdentity is who the stamp says is asking: owner is true only when every owner check passes.
type callerIdentity struct {
	owner  bool
	pseudo string // the stamped hub user when it has the stamp's form; "" otherwise
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
	tunnel.OwnerRouteKey("PUT", "/api/board/{project}/settings"):                    "only the account this computer is linked to can change board settings through Coral Hub",
	tunnel.OwnerRouteKey("GET", tunnel.PromptInspectionPath):                        "only the account this computer is linked to can read the prompts through Coral Hub",
	tunnel.OwnerRouteKey("GET", "/api/board/{project}/cross-team/settings"):         textHubSwitchOwnerOnly,
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

const textSavedOwnerOnly = "through Coral Hub the saved messages are for the account this computer is linked to only (#5753)"

const textHubAutoAcceptOwnerOnly = "through Coral Hub only the account this computer is linked to can turn Coral Hub auto-accept on or off; on this computer use Settings > Network and Hub"

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
	text, ok := ownerRefusals[tunnel.OwnerRouteKey(r.Method, pattern)]
	if !ok {
		text = "through Coral Hub this is for the account this computer is linked to only"
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"code": "owner_only", "error": text})
	return true
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
