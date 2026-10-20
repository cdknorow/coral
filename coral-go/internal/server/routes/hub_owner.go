// Who is the machine's owner behind a Coral Hub request (#5280, #5307): the checks of section 13, shared by the update routes and the owner's
// configuration routes (a board's health check setup, the prompt inspection).

package routes

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"sync"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
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

// hubOwnerRequest is identifyOwner with the server's enrolment record.
func hubOwnerRequest(r *http.Request) callerIdentity {
	return identifyOwner(r, hubOwnerLabel, &hubOwnerNoLabelLog)
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
