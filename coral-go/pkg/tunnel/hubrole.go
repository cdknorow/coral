package tunnel

import (
	"context"
	"net/http"
	"regexp"
)

// The owner marker for the update routes (#5280, section 13 of the auto-update design) and the owner's configuration routes (#5307).
//
// Coral Hub marks a request "from the account this machine is enrolled to" only on those routes, as a field of the stream
// (Header.HubRole), never as an HTTP header a browser could send. The connector turns the field into HubRoleHeader for the local
// server on those two routes and nowhere else; the server honours it only on a request this connector stamped. Any HubRoleHeader a
// request carries itself is not on the forward allowlist, and is deleted again after the scrub.

// HubRoleHeader is the local header the connector sets from the stream's HubRole.
const HubRoleHeader = "X-Coral-Hub-Role"

// HubRoleOwner is the only role there is.
const HubRoleOwner = "owner"

// The two update routes, as the route table lists them.
const (
	UpdateStatusPath   = "/api/system/update"
	UpdateRelaunchPath = "/api/system/update/relaunch"
)

// IsOwnerRoute reports whether the owner marker is stamped for (method, decodedPath): the two update routes, and the owner's reads and
// writes of this computer's own configuration (#5307): GET and PUT /api/board/{project}/settings (the health check's setup) and
// GET /api/settings/prompt-inspection. The marker only says the hub account is the one this computer is linked to; every one of these routes
// decides for itself what it gives the owner and refuses a viewer without it.
func IsOwnerRoute(method, decodedPath string) bool {
	if IsUpdateRoute(method, decodedPath) {
		return true
	}
	switch method {
	case http.MethodGet:
		return decodedPath == PromptInspectionPath || boardSettingsRoute.MatchString(decodedPath)
	case http.MethodPut:
		return boardSettingsRoute.MatchString(decodedPath)
	}
	return false
}

// PromptInspectionPath is the read-only view of the global prompts.
const PromptInspectionPath = "/api/settings/prompt-inspection"

var boardSettingsRoute = regexp.MustCompile(`^/api/board/[^/]+/settings$`)

// IsUpdateRoute reports whether (method, decodedPath) is GET /api/system/update or POST /api/system/update/relaunch.
func IsUpdateRoute(method, decodedPath string) bool {
	return (method == http.MethodGet && decodedPath == UpdateStatusPath) ||
		(method == http.MethodPost && decodedPath == UpdateRelaunchPath)
}

type hubRoleKey struct{}

// WithHubRole returns ctx carrying the viewer's role. Session.RoundTrip sends it with the stream. Only "owner" is kept; anything
// else is ignored.
func WithHubRole(ctx context.Context, role string) context.Context {
	if role != HubRoleOwner {
		return ctx
	}
	return context.WithValue(ctx, hubRoleKey{}, role)
}

func hubRoleFrom(ctx context.Context) string {
	r, _ := ctx.Value(hubRoleKey{}).(string)
	return r
}

// stampRole sets HubRoleHeader (and the stamped hub user, which the server compares with the one recorded at enrolment) on
// the request headers of a stream for an update route whose stream says owner. Any other stream, and any other route, gets neither:
// whatever the request itself carried under these names is removed first.
func stampRole(out http.Header, h Header, method, decodedPath string) {
	out.Del(HubRoleHeader)
	if h.HubRole == HubRoleOwner && IsOwnerRoute(method, decodedPath) {
		out.Set(HubRoleHeader, HubRoleOwner)
		out.Del(HubUserHeader)
		if h.HubUser != "" {
			out.Set(HubUserHeader, h.HubUser)
		}
	}
}
