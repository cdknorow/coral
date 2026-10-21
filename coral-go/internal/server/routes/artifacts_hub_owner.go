package routes

// Through Coral Hub the artifacts and the images pasted in the dashboard are the machine's OWNER's alone (operator decision, 2026-10-21, #5575): what an agent made or a person pasted can hold
// anything, so a grantee, a view-only user or an agent reaches none of it, whatever tier the route has. The hub refuses those callers too; this is the machine's own check (defence in depth),
// the same owner check as the configuration routes (hubOwnerRequest, #5307): the stamped marker and the hub user must be the enrolled owner's, and the enrolment must have recorded the
// owner's label. This computer's dashboard and the command line are not affected.

import (
	"net/http"

	"github.com/subgentic/coral/internal/cloud"
)

// refuseArtifactsToNonOwner answers 403 owner_only to a request that came through Coral Hub from anyone but the owner, and says it did.
func refuseArtifactsToNonOwner(w http.ResponseWriter, r *http.Request) bool {
	if !cloud.Tunneled(r) {
		return false
	}
	if !hubOwnerRequest(r).owner {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "owner_only", "error": "through Coral Hub artifacts and pasted images are for the account this computer is linked to only"})
		return true
	}
	return false
}
