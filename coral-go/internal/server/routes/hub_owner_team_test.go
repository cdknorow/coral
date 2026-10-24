package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #7061: a machine enrolled to a Coral Hub organisation. Its owners are the organisation's owners and admins; the personal owner marker means nothing there, and nothing is ever read from a
// request but the connector's stamp.

const (
	teamOrgA = "0b9f1c2e-3d4a-4b5c-8d6e-7f8091a2b3c4"
	teamOrgB = "ffffffff-0000-4000-8000-000000000001"
	teamUIDA = "hub-uid-0123456789abcdef0123456789abcdef"
	teamUIDB = "hub-uid-fedcba9876543210fedcba9876543210"
	teamUser = "hub-user-1234abcd"
)

func withRecord(t *testing.T, kind, org string, err error) {
	t.Helper()
	old := hubOwnerRecord
	hubOwnerRecord = func(context.Context) (string, string, error) { return kind, org, err }
	t.Cleanup(func() { hubOwnerRecord = old })
}

// teamStamp is the connector's stamp for a team request, as headers.
func teamStamp(org, role, uid, user string) http.Header {
	h := http.Header{}
	h.Set(tunnel.ViaHeader, cloud.ViaSecret())
	if org != "" {
		h.Set(tunnel.HubOrgHeader, org)
	}
	if role != "" {
		h.Set(tunnel.HubTeamRoleHeader, role)
	}
	if uid != "" {
		h.Set(tunnel.HubUserIDHeader, uid)
	}
	if user != "" {
		h.Set(tunnel.HubUserHeader, user)
	}
	return h
}

// ownerRouteCall sends a GET /api/artifacts (an owner route of the table) through refuseNonOwner, and says whether the request was let in and, if refused, the code.
func ownerRouteCall(t *testing.T, hdr http.Header) (allowed bool, code string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts", nil)
	for k, vs := range hdr {
		req.Header[k] = vs
	}
	rec := httptest.NewRecorder()
	if refuseNonOwner(rec, req) {
		require.Equal(t, http.StatusForbidden, rec.Code)
		return false, decodeStrings(t, rec.Body.String())["code"]
	}
	return true, ""
}

func TestTeamMachineOwnerTruthTable(t *testing.T) {
	type rec struct {
		kind, org string
		err       error
	}
	teamA := rec{"team", teamOrgA, nil}
	good := func() http.Header { return teamStamp(teamOrgA, "admin", teamUIDA, teamUser) }
	with := func(mod func(http.Header)) http.Header { h := good(); mod(h); return h }
	for _, tc := range []struct {
		name string
		rec  rec
		hdr  http.Header
		want bool
	}{
		{"an admin of the organisation", teamA, good(), true},
		{"an owner of the organisation", teamA, teamStamp(teamOrgA, "owner", teamUIDB, teamUser), true},
		{"a member", teamA, teamStamp(teamOrgA, "member", teamUIDA, teamUser), false},
		{"role in another case", teamA, teamStamp(teamOrgA, "Admin", teamUIDA, teamUser), false},
		{"role with a space", teamA, teamStamp(teamOrgA, "admin ", teamUIDA, teamUser), false},
		{"role as a list", teamA, teamStamp(teamOrgA, "owner,admin", teamUIDA, teamUser), false},
		{"an unknown role", teamA, teamStamp(teamOrgA, "root", teamUIDA, teamUser), false},
		{"no role", teamA, teamStamp(teamOrgA, "", teamUIDA, teamUser), false},
		{"a repeated role", teamA, with(func(h http.Header) { h.Add(tunnel.HubTeamRoleHeader, "admin") }), false},
		{"another organisation", teamA, teamStamp(teamOrgB, "admin", teamUIDA, teamUser), false},
		{"the organisation in upper case", teamA, teamStamp(strings.ToUpper(teamOrgA), "admin", teamUIDA, teamUser), false},
		{"the organisation without hyphens", teamA, teamStamp(strings.ReplaceAll(teamOrgA, "-", ""), "admin", teamUIDA, teamUser), false},
		{"no organisation", teamA, teamStamp("", "admin", teamUIDA, teamUser), false},
		{"a repeated organisation", teamA, with(func(h http.Header) { h.Add(tunnel.HubOrgHeader, teamOrgA) }), false},
		{"no user id", teamA, teamStamp(teamOrgA, "admin", "", teamUser), false},
		{"a malformed user id", teamA, teamStamp(teamOrgA, "admin", "hub-uid-1234", teamUser), false},
		{"an upper-case user id", teamA, teamStamp(teamOrgA, "admin", strings.ToUpper(teamUIDA), teamUser), false},
		{"a repeated user id", teamA, with(func(h http.Header) { h.Add(tunnel.HubUserIDHeader, teamUIDB) }), false},
		{"no label", teamA, teamStamp(teamOrgA, "admin", teamUIDA, ""), false},
		{"a malformed label", teamA, teamStamp(teamOrgA, "admin", teamUIDA, "alice"), false},
		{"a repeated label", teamA, with(func(h http.Header) { h.Add(tunnel.HubUserHeader, "hub-user-ffffffff") }), false},
		{"a forged connector stamp", teamA, with(func(h http.Header) { h.Set(tunnel.ViaHeader, "forged") }), false},
		{"the personal owner marker alone", teamA, func() http.Header {
			h := teamStamp("", "", "", teamUser)
			h.Set(tunnel.HubRoleHeader, tunnel.HubRoleOwner)
			return h
		}(), false},
		{"the personal marker with a stamp that is not complete", teamA, func() http.Header {
			h := teamStamp(teamOrgA, "admin", "", teamUser)
			h.Set(tunnel.HubRoleHeader, tunnel.HubRoleOwner)
			return h
		}(), false},
		{"a complete stamp and the marker too", teamA, with(func(h http.Header) { h.Set(tunnel.HubRoleHeader, tunnel.HubRoleOwner) }), true},
		{"a record that cannot be read", rec{"", "", errors.New("db")}, good(), false},
		{"a team record with no organisation", rec{"team", "", nil}, good(), false},
		{"a team record with a malformed organisation", rec{"team", strings.ToUpper(teamOrgA), nil}, good(), false},
		{"a kind this code does not know", rec{"organisation", teamOrgA, nil}, good(), false},
		{"a kind this code does not know, with the personal marker", rec{"organisation", "", nil}, func() http.Header {
			h := teamStamp("", "", "", teamUser)
			h.Set(tunnel.HubRoleHeader, tunnel.HubRoleOwner)
			return h
		}(), false},
	} {
		resetOwnerState(t)
		withLabel(t, "", nil) // a team machine has no owner label: never needed, never consulted
		withRecord(t, tc.rec.kind, tc.rec.org, tc.rec.err)
		allowed, code := ownerRouteCall(t, tc.hdr)
		assert.Equal(t, tc.want, allowed, tc.name)
		if !tc.want {
			assert.Equal(t, "owner_only", code, tc.name+": a refusal in the route's words, never the 'confirm owner' prompt")
		}
	}
}

// A request that did not come through the connector carries no stamp that counts: its team headers make no owner (this computer's own requests are the dashboard's business, not the Hub's).
func TestTeamHeadersWithoutTheConnectorStampMakeNoOwner(t *testing.T) {
	resetOwnerState(t)
	withLabel(t, "", nil)
	withRecord(t, "team", teamOrgA, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts", nil)
	req.Header = teamStamp(teamOrgA, "admin", teamUIDA, teamUser)
	req.Header.Del(tunnel.ViaHeader)
	assert.False(t, hubOwnerRequest(req).owner)
	req.Header.Set(tunnel.ViaHeader, "forged")
	assert.False(t, hubOwnerRequest(req).owner)
}

// The personal truth table is what it was: the marker, the hub user and the enrolled label decide, and a team stamp on a personal machine is ignored.
func TestPersonalMachineIgnoresTheTeamStamp(t *testing.T) {
	personal := func(h http.Header) http.Header {
		h.Set(tunnel.HubRoleHeader, tunnel.HubRoleOwner)
		h.Set(tunnel.HubUserHeader, ownerLabel)
		return h
	}
	for _, tc := range []struct {
		name  string
		label string
		hdr   http.Header
		want  bool
	}{
		{"the owner", ownerLabel, personal(teamStamp("", "", "", "")), true},
		{"the owner, with a team stamp too", ownerLabel, personal(teamStamp(teamOrgA, "admin", teamUIDA, "")), true},
		{"a team stamp alone", ownerLabel, teamStamp(teamOrgA, "owner", teamUIDA, ownerLabel), false},
		{"a team stamp that matches nothing, with the marker", ownerLabel, personal(teamStamp(teamOrgB, "member", "junk", "")), true},
		{"the owner of another label", "hub-user-9999ffff", personal(teamStamp("", "", "", "")), false},
		{"no recorded label", "", personal(teamStamp("", "", "", "")), false},
	} {
		resetOwnerState(t)
		withLabel(t, tc.label, nil)
		withRecord(t, "personal", "", nil)
		allowed, _ := ownerRouteCall(t, tc.hdr)
		assert.Equal(t, tc.want, allowed, tc.name)
		// With no source wired at all, the same.
		hubOwnerRecord = nil
		allowed, _ = ownerRouteCall(t, tc.hdr)
		assert.Equal(t, tc.want, allowed, tc.name+" (no record source)")
	}
}

// Every owner-only route refuses a team machine whose record gives no organisation, and a stamp for another one: the census is the route table's own refusal list.
func TestEveryOwnerRouteRefusesATeamMachineWithNoOrganisationOrAnotherOne(t *testing.T) {
	resetOwnerState(t)
	withLabel(t, "", nil)
	require.NotEmpty(t, ownerRefusals)
	for _, record := range []struct {
		name, kind, org string
		err             error
	}{{"no organisation", "team", "", nil}, {"unreadable", "", "", errors.New("db")}, {"another organisation", "team", teamOrgB, nil}} {
		withRecord(t, record.kind, record.org, record.err)
		for key := range ownerRefusals {
			method, pattern, _ := strings.Cut(key, " ")
			path := strings.NewReplacer("{hex64}", strings.Repeat("a", 64), "{id}", "1", "{project}", "team").Replace(pattern)
			req := httptest.NewRequest(method, path, nil)
			req.Header = teamStamp(teamOrgA, "owner", teamUIDA, teamUser)
			rec := httptest.NewRecorder()
			assert.True(t, refuseNonOwner(rec, req), "%s: %s", record.name, key)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", record.name, key)
		}
	}
	// And with the right organisation every one of them lets the stamped owner in.
	withRecord(t, "team", teamOrgA, nil)
	for key := range ownerRefusals {
		method, pattern, _ := strings.Cut(key, " ")
		path := strings.NewReplacer("{hex64}", strings.Repeat("a", 64), "{id}", "1", "{project}", "team").Replace(pattern)
		req := httptest.NewRequest(method, path, nil)
		req.Header = teamStamp(teamOrgA, "admin", teamUIDA, teamUser)
		assert.False(t, refuseNonOwner(httptest.NewRecorder(), req), key)
	}
}

// The update routes accept the marker alone on a personal machine (no label needed); on a team machine they need the organisation, and a record that is not understood refuses them too.
func TestTeamMachineOnTheUpdateRoutes(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		org  string
		err  error
		hdr  http.Header
		want bool
	}{
		{"team, an admin", "team", teamOrgA, nil, teamStamp(teamOrgA, "admin", teamUIDA, teamUser), true},
		{"team, a member", "team", teamOrgA, nil, teamStamp(teamOrgA, "member", teamUIDA, teamUser), false},
		{"team, another organisation", "team", teamOrgA, nil, teamStamp(teamOrgB, "owner", teamUIDA, teamUser), false},
		{"team, no organisation on record", "team", "", nil, teamStamp(teamOrgA, "owner", teamUIDA, teamUser), false},
		{"team, record unreadable", "", "", errors.New("db"), teamStamp(teamOrgA, "owner", teamUIDA, teamUser), false},
		{"team, the personal marker", "team", teamOrgA, nil, func() http.Header {
			h := teamStamp("", "", "", teamUser)
			h.Set(tunnel.HubRoleHeader, tunnel.HubRoleOwner)
			return h
		}(), false},
		{"personal, the marker alone", "personal", "", nil, func() http.Header {
			h := teamStamp("", "", "", teamUser)
			h.Set(tunnel.HubRoleHeader, tunnel.HubRoleOwner)
			return h
		}(), true},
	} {
		resetOwnerState(t)
		withLabel(t, "", nil)
		withRecord(t, tc.kind, tc.org, tc.err)
		req := httptest.NewRequest(http.MethodGet, tunnel.UpdateStatusPath, nil)
		req.Header = tc.hdr
		assert.Equal(t, tc.want, hubOwnerRequest(req).owner, tc.name)
	}
}

// The identity a team stamp yields: the organisation is the record's, the id and label are the stamp's, and none of it is a personal machine's.
func TestTeamIdentityCarriesTheRecordedOrganisationAndTheStampedPerson(t *testing.T) {
	resetOwnerState(t)
	withLabel(t, "", nil)
	withRecord(t, "team", teamOrgA, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts", nil)
	req.Header = teamStamp(teamOrgA, "admin", teamUIDB, teamUser)
	id := hubOwnerRequest(req)
	require.True(t, id.owner)
	assert.True(t, id.team)
	assert.Equal(t, teamOrgA, id.org)
	assert.Equal(t, teamUIDB, id.hubUserID)
	assert.Equal(t, teamUser, id.pseudo)
	assert.False(t, id.local)

	withRecord(t, "personal", "", nil)
	withLabel(t, ownerLabel, nil)
	id = hubOwnerRequest(httptest.NewRequest(http.MethodGet, "/api/artifacts", nil))
	assert.False(t, id.owner, "no stamp, no owner")
	req = httptest.NewRequest(http.MethodGet, "/api/artifacts", nil)
	for k, v := range ownerHeaders(ownerLabel) {
		req.Header.Set(k, v)
	}
	id = hubOwnerRequest(req)
	assert.True(t, id.owner)
	assert.False(t, id.team)
	assert.Empty(t, id.org)
	assert.Empty(t, id.hubUserID)
}

// A team's machine has no person to confirm: nothing is "missing", Confirm owner answers 409, an armed adoption records nothing from the personal marker, and the status says "team" without the
// organisation.
func TestTeamMachineOffersNoOwnerConfirmation(t *testing.T) {
	e := newOwnerLabelEnv(t)
	e.store.kind, e.store.org = "team", teamOrgA
	hubOwnerRecord = e.store.OwnerRecord
	t.Cleanup(func() { hubOwnerRecord = nil })

	missing, known := e.h.ownerLabelState(context.Background())
	assert.False(t, missing, "an empty label is nothing missing on a team's machine")
	assert.True(t, known)

	rec := e.arm()
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "team_machine", decodeStrings(t, rec.Body.String())["code"])

	rec = e.call(http.MethodGet, "/api/cloud/owner-label", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"label_missing":false`)

	// Even armed (a second path to the same function), the personal marker with a valid user adopts nothing.
	_, ok, _ := armOwnerAdoption(ownerNow())
	require.True(t, ok)
	// The personal marker, and a complete team stamp (which the owner check passes: the adoption's own guard is what stops it recording the person as the owner).
	for name, hdr := range map[string]http.Header{
		"the personal marker": func() http.Header {
			h := http.Header{}
			for k, v := range ownerHeaders(ownerLabel) {
				h.Set(k, v)
			}
			return h
		}(),
		"a complete team stamp": teamStamp(teamOrgA, "admin", teamUIDA, ownerLabel),
	} {
		_, ok, _ = armOwnerAdoption(ownerNow())
		require.True(t, ok, name)
		req := httptest.NewRequest(http.MethodGet, "/api/custom-models", nil)
		req.Header = hdr
		assert.False(t, adoptOwnerFromRequest(req), name)
		assert.False(t, ownerLabelMissingFor(req), name)
	}
	assert.Empty(t, e.store.adopted)
	assert.Empty(t, e.store.label)

	st := e.h.status()
	assert.Equal(t, "team", st.OwnerKind)
	assert.False(t, st.OwnerLabelMissing)
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), teamOrgA, "the organisation's id is not in the status")
}

// An unreadable record offers nothing either: it is neither personal nor a team's, and no label is adopted from a request that arrives while it is unreadable.
func TestUnreadableRecordAdoptsNothing(t *testing.T) {
	e := newOwnerLabelEnv(t)
	e.store.recErr = errors.New("db")
	hubOwnerRecord = e.store.OwnerRecord
	t.Cleanup(func() { hubOwnerRecord = nil })
	_, known := e.h.ownerLabelState(context.Background())
	assert.False(t, known)
	_, ok, _ := armOwnerAdoption(ownerNow())
	require.True(t, ok)
	req := httptest.NewRequest(http.MethodGet, "/api/custom-models", nil)
	for k, v := range ownerHeaders(ownerLabel) {
		req.Header.Set(k, v)
	}
	assert.False(t, adoptOwnerFromRequest(req))
	assert.Empty(t, e.store.adopted)
}

// A personal machine is as before: the status says "personal".
func TestPersonalMachineStatusSaysPersonal(t *testing.T) {
	e := newOwnerLabelEnv(t)
	assert.Equal(t, "personal", e.h.status().OwnerKind)
}
