package tunnel

import (
	"context"
	"net/http"
	"regexp"
)

// Team-owned machines (#7061). A machine enrolled to a Coral Hub organisation, not to one person, has no single owner: the owners and admins of that organisation reach the owner routes. Coral Hub says
// who is asking as three fields of the stream (Header.HubOrg, HubTeamRole, HubUserID), never as an HTTP header a browser could send; the connector turns them into the headers below on an owner route
// (ownerroutes.go) and nowhere else, and the server honours them only on a request this connector stamped, on a machine enrolled as a team's, with the organisation it recorded at enrolment.

// The local headers the connector sets from the stream's team fields.
const (
	HubOrgHeader      = "X-Coral-Hub-Org"
	HubTeamRoleHeader = "X-Coral-Hub-Team-Role"
	HubUserIDHeader   = "X-Coral-Hub-User-Id"
)

// The roles an organisation member can have at the hub. Only the first two pass an owner route (the server's rule, not the stream's): a member, and anything the hub invents later, is refused.
const (
	HubTeamRoleOwner  = "owner"
	HubTeamRoleAdmin  = "admin"
	HubTeamRoleMember = "member"
)

// CapTeamOwner is the capability "team_owner/1" in Hello.Capabilities (#7061): this Coral knows team-owned machines (owner_kind, hub_org, hub_team_role, hub_user_id). The hub enrols a machine as a
// team's only when it says so, so an older Coral is never left with owner routes it answers 403 to.
const CapTeamOwner = "team_owner/1"

// The two kinds of owner a machine can be enrolled to (Hello.OwnerKind). An enrolment that does not say is personal.
const (
	OwnerKindPersonal = "personal"
	OwnerKindTeam     = "team"
)

var (
	hubOrgValue    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hubUserIDValue = regexp.MustCompile(`^hub-uid-[0-9a-f]{32}$`)
)

// ValidHubOrg reports whether s is an organisation id as the hub writes it: the canonical lower-case UUID, 8-4-4-4-12 with hyphens, 36 bytes, no prefix and no braces. Any other spelling (upper case, no
// hyphens, another length, empty) is not one and never matches one, so ids are compared as exact bytes.
func ValidHubOrg(s string) bool { return len(s) == 36 && hubOrgValue.MatchString(s) }

// ValidHubUserID reports whether s is a stable Coral Hub user id, hub-uid- and 32 lower-case hex digits (40 bytes). It is pseudonymous and the same for an account on every machine, and it is
// scoped by organisation here: the same person in two organisations is two ids to Coral, and nothing assumes otherwise.
func ValidHubUserID(s string) bool { return len(s) == 40 && hubUserIDValue.MatchString(s) }

// ValidHubTeamRole reports whether s is one of the three roles, exactly (no case folding, no spaces, no lists).
func ValidHubTeamRole(s string) bool {
	return s == HubTeamRoleOwner || s == HubTeamRoleAdmin || s == HubTeamRoleMember
}

// HubTeam is who a team-machine request is for, as the hub states it: the organisation, the person's role in it and their stable id.
type HubTeam struct {
	Org, Role, UserID string
}

type hubTeamKey struct{}

// WithHubTeam returns ctx carrying the viewer's organisation, role and stable id. Session.RoundTrip sends them with the stream. All three must be well formed, or none is kept: a half-stamp is
// no stamp.
func WithHubTeam(ctx context.Context, t HubTeam) context.Context {
	if !ValidHubOrg(t.Org) || !ValidHubTeamRole(t.Role) || !ValidHubUserID(t.UserID) {
		return ctx
	}
	return context.WithValue(ctx, hubTeamKey{}, t)
}

func hubTeamFrom(ctx context.Context) HubTeam {
	t, _ := ctx.Value(hubTeamKey{}).(HubTeam)
	return t
}

// stampTeam sets the three team headers, and the stamped hub user (the label the person is shown by), on a request for an owner route whose stream carries a complete team stamp. Any other
// stream, and any other route, gets none of them: whatever the request itself carried under these names is removed first.
func stampTeam(out http.Header, h Header, method, decodedPath string) {
	out.Del(HubOrgHeader)
	out.Del(HubTeamRoleHeader)
	out.Del(HubUserIDHeader)
	if !IsOwnerRoute(method, decodedPath) {
		return
	}
	if !ValidHubOrg(h.HubOrg) || !ValidHubTeamRole(h.HubTeamRole) || !ValidHubUserID(h.HubUserID) {
		return
	}
	out.Set(HubOrgHeader, h.HubOrg)
	out.Set(HubTeamRoleHeader, h.HubTeamRole)
	out.Set(HubUserIDHeader, h.HubUserID)
	out.Del(HubUserHeader)
	if h.HubUser != "" {
		out.Set(HubUserHeader, h.HubUser)
	}
}

// currentOwnerKind is what fn reports, or personal when there is no fn or it says anything but "team".
func currentOwnerKind(fn func() string) string {
	if fn != nil && fn() == OwnerKindTeam {
		return OwnerKindTeam
	}
	return OwnerKindPersonal
}

// wellFormedOwnerKind is kind when it is one of the two, else "".
func wellFormedOwnerKind(kind string) string {
	if kind == OwnerKindPersonal || kind == OwnerKindTeam {
		return kind
	}
	return ""
}
