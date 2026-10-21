package tunnel

import "net/http"

// The owner's routes: ONE place says which routes are the computer owner's (#5610, after the stamp defect of #5575: the connector's own list and the server's checks had drifted).
//
// Coral Hub marks a request "from the account this machine is enrolled to" as a field of the stream, never as an HTTP header a browser could send. The connector turns the field into
// HubRoleHeader for the local server on the routes of THIS table and nowhere else, and the server asks the same table which routes need the owner and what a non-owner gets there.
// Every owner route is in routeTable (routes.go) with its tier, marked by owner(...): there is no second list to forget a route in. The marker only says the hub account is the one this
// computer is linked to; the machine decides the rest (the enrolled label, the local locks).

// OwnerMode says how the owner marker applies to a route.
type OwnerMode int

const (
	// OwnerNone: not an owner route. No marker is stamped.
	OwnerNone OwnerMode = iota
	// OwnerRefuse: the owner's alone through the hub. The marker is stamped, and the machine refuses anyone else (403 owner_only) before the handler does anything. The enrolled owner
	// label is required: a machine linked before it was recorded refuses even the owner until it is linked again.
	OwnerRefuse
	// OwnerHubOnly: OwnerRefuse, and the machine also refuses a request that did not come through Coral Hub (the cross-team switch routes are the Hub owner's way in; a person here uses the
	// settings route).
	OwnerHubOnly
	// OwnerSees: the marker is stamped and the handler decides what a non-owner gets (a board's settings leave the health setup out for anyone but the owner). The label is required.
	OwnerSees
	// OwnerUpdate: the update routes (#5280). The marker is stamped, the handler decides what a non-owner gets, and a machine linked before the owner label was recorded still accepts the
	// marker alone (the label is optional).
	OwnerUpdate
)

// Stamped reports whether the connector stamps the owner marker on routes of this mode.
func (m OwnerMode) Stamped() bool { return m != OwnerNone }

// Refuses reports whether the machine refuses a non-owner on routes of this mode before the handler runs.
func (m OwnerMode) Refuses() bool { return m == OwnerRefuse || m == OwnerHubOnly }

// RefusesLocal reports whether the machine also refuses a request that did not come through Coral Hub, on routes of this mode.
func (m OwnerMode) RefusesLocal() bool { return m == OwnerHubOnly }

// LabelRequired reports whether the owner check needs the owner's label recorded at enrolment.
func (m OwnerMode) LabelRequired() bool {
	return m == OwnerRefuse || m == OwnerHubOnly || m == OwnerSees
}

func (m OwnerMode) String() string {
	switch m {
	case OwnerRefuse:
		return "owner-only"
	case OwnerHubOnly:
		return "owner-only-hub"
	case OwnerSees:
		return "owner-sees"
	case OwnerUpdate:
		return "owner-update"
	}
	return ""
}

// OwnerRoute returns the table pattern and owner mode of a request, ignoring its query (the first matching rule decides, as for the tier). OwnerNone for a route that is not an owner
// route or is not in the table.
func OwnerRoute(method, decodedPath string) (pattern string, mode OwnerMode) {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	segs, ok := splitSegments(decodedPath)
	if !ok {
		return "", OwnerNone
	}
	for _, r := range routeTable {
		if methodMatches(r.method, method) && matchSegments(r.pattern, segs) {
			return r.pattern, r.owner
		}
	}
	return "", OwnerNone
}

// IsOwnerRoute reports whether the owner marker is stamped for (method, decodedPath): the routes of the table marked as the owner's.
func IsOwnerRoute(method, decodedPath string) bool {
	_, m := OwnerRoute(method, decodedPath)
	return m.Stamped()
}

// OwnerRouteInfo is one owner route of the table.
type OwnerRouteInfo struct {
	Method, Pattern string
	Tier            Tier
	Mode            OwnerMode
}

// OwnerRoutes lists every owner route of the table, for the census tests.
func OwnerRoutes() []OwnerRouteInfo {
	var out []OwnerRouteInfo
	for _, r := range routeTable {
		if r.owner != OwnerNone {
			out = append(out, OwnerRouteInfo{Method: r.method, Pattern: r.pattern, Tier: r.tier, Mode: r.owner})
		}
	}
	return out
}

// OwnerRouteKey is "METHOD pattern", the key of a route in the owner tables of the server (its refusal texts).
func OwnerRouteKey(method, pattern string) string { return method + " " + pattern }
