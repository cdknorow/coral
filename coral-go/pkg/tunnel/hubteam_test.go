package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	teamOrg  = "0b9f1c2e-3d4a-4b5c-8d6e-7f8091a2b3c4"
	teamUID  = "hub-uid-0123456789abcdef0123456789abcdef"
	teamUser = "hub-user-1234abcd"
)

// #7061: the organisation id is the canonical lower-case UUID and nothing else, compared as exact bytes.
func TestHubTeamValuesAreExactlyTheirShapes(t *testing.T) {
	for _, ok := range []string{teamOrg, "00000000-0000-0000-0000-000000000000", "ffffffff-ffff-ffff-ffff-ffffffffffff"} {
		assert.True(t, ValidHubOrg(ok), ok)
	}
	for _, bad := range []string{"", strings.ToUpper(teamOrg), strings.ReplaceAll(teamOrg, "-", ""), "{" + teamOrg + "}", "urn:uuid:" + teamOrg, teamOrg + " ", " " + teamOrg,
		teamOrg + "\n", teamOrg[:35], teamOrg + "0", "hub-org-1234abcd", "0b9f1c2e-3d4a-4b5c-8d6e-7f8091a2b3cg", "0B9f1c2e-3d4a-4b5c-8d6e-7f8091a2b3c4"} {
		assert.False(t, ValidHubOrg(bad), "%q", bad)
	}
	assert.True(t, ValidHubUserID(teamUID))
	for _, bad := range []string{"", "hub-uid-", teamUID + "0", teamUID[:39], strings.ToUpper(teamUID), "hub-user-1234abcd", " " + teamUID, teamUID + "\n", "hub-uid-0123456789abcdef0123456789abcdeg"} {
		assert.False(t, ValidHubUserID(bad), "%q", bad)
	}
	for _, ok := range []string{"owner", "admin", "member"} {
		assert.True(t, ValidHubTeamRole(ok), ok)
	}
	for _, bad := range []string{"", "Owner", "ADMIN", "admin ", " member", "owner,admin", "viewer", "guest", "root"} {
		assert.False(t, ValidHubTeamRole(bad), "%q", bad)
	}
}

func TestHubTeamFieldsSurviveValidationOnlyWellFormed(t *testing.T) {
	l := Limits{}.withDefaults()
	ok := Header{Kind: KindHTTP, Method: "GET", Path: "/x", HubOrg: teamOrg, HubTeamRole: "admin", HubUserID: teamUID}
	assert.NoError(t, ok.validate(l))
	for name, mod := range map[string]func(*Header){
		"bad org":            func(h *Header) { h.HubOrg = strings.ToUpper(teamOrg) },
		"bad role":           func(h *Header) { h.HubTeamRole = "Admin" },
		"bad id":             func(h *Header) { h.HubUserID = "hub-uid-zz" },
		"a role with no org": func(h *Header) { h.HubOrg = "" },
	} {
		h := ok
		mod(&h)
		assert.ErrorIs(t, h.validate(l), ErrBadHeader, name)
	}
	// The stream may carry the org alone, or the id alone: only the server decides what is enough.
	onlyOrg := Header{Kind: KindHTTP, Method: "GET", Path: "/x", HubOrg: teamOrg}
	assert.NoError(t, onlyOrg.validate(l))
}

func TestWithHubTeamKeepsAllThreeOrNone(t *testing.T) {
	good := HubTeam{Org: teamOrg, Role: "owner", UserID: teamUID}
	assert.Equal(t, good, hubTeamFrom(WithHubTeam(context.Background(), good)))
	for name, bad := range map[string]HubTeam{
		"no id":    {Org: teamOrg, Role: "owner"},
		"no org":   {Role: "owner", UserID: teamUID},
		"no role":  {Org: teamOrg, UserID: teamUID},
		"bad role": {Org: teamOrg, Role: "boss", UserID: teamUID},
		"bad org":  {Org: "x", Role: "owner", UserID: teamUID},
	} {
		assert.Equal(t, HubTeam{}, hubTeamFrom(WithHubTeam(context.Background(), bad)), name)
	}
}

// A stream that carries only the team fields is not an "empty" header: the hub-opened checks on the connector-opened streams refuse it.
func TestTeamFieldsAreNotAnEmptyHeader(t *testing.T) {
	art := Header{Kind: KindRelay, Method: http.MethodPost, Path: RelayArtifactPath, ContentLength: 5}
	require.True(t, validArtifactHeader(art, 100))
	for name, mod := range map[string]func(*Header){
		"org":  func(h *Header) { h.HubOrg = teamOrg },
		"role": func(h *Header) { h.HubTeamRole = "owner" },
		"id":   func(h *Header) { h.HubUserID = teamUID },
	} {
		h := art
		mod(&h)
		assert.False(t, validArtifactHeader(h, 100), "artifact: "+name)
	}
	for _, tc := range []struct {
		k    *inboundKind
		path string
	}{{relayKind, RelayMessagePath}, {controlKind, ControlModePath}} {
		h := Header{Kind: tc.k.kind, Method: http.MethodPost, Path: tc.path, ContentLength: 1}
		require.True(t, validInboundHeader(tc.k, h), tc.path)
		for name, mod := range map[string]func(*Header){
			"org":  func(h *Header) { h.HubOrg = teamOrg },
			"role": func(h *Header) { h.HubTeamRole = "owner" },
			"id":   func(h *Header) { h.HubUserID = teamUID },
		} {
			bad := h
			mod(&bad)
			assert.False(t, validInboundHeader(tc.k, bad), "%s: %s", tc.k.kind, name)
		}
	}
}

func TestStampTeamOnlyACompleteStampOnAnOwnerRoute(t *testing.T) {
	full := Header{Kind: KindHTTP, HubUser: teamUser, HubOrg: teamOrg, HubTeamRole: "admin", HubUserID: teamUID}
	forged := http.Header{
		HubOrgHeader: {"other"}, "x-coral-hub-org": {"other"}, HubTeamRoleHeader: {"owner"}, "X-CORAL-HUB-TEAM-ROLE": {"owner"}, HubUserIDHeader: {"hub-uid-ffffffffffffffffffffffffffffffff"},
	}
	for _, tc := range []struct {
		name         string
		mod          func(*Header)
		method, path string
		in           http.Header
		want         bool
	}{
		{"admin on the relaunch", nil, http.MethodPost, "/api/system/update/relaunch", nil, true},
		{"admin on an artifact read", nil, http.MethodGet, "/api/artifacts/" + hexID, nil, true},
		{"admin on the artifact list", nil, http.MethodGet, "/api/artifacts", nil, true},
		{"a member's stamp is carried; the server refuses it", func(h *Header) { h.HubTeamRole = "member" }, http.MethodGet, "/api/artifacts", nil, true},
		{"not an owner route", nil, http.MethodGet, "/api/sessions/live", nil, false},
		{"a board write", nil, http.MethodPost, "/api/board/t/messages", nil, false},
		{"no org", func(h *Header) { h.HubOrg = "" }, http.MethodGet, "/api/artifacts", nil, false},
		{"no role", func(h *Header) { h.HubTeamRole = "" }, http.MethodGet, "/api/artifacts", nil, false},
		{"no id", func(h *Header) { h.HubUserID = "" }, http.MethodGet, "/api/artifacts", nil, false},
		{"a malformed id", func(h *Header) { h.HubUserID = "hub-uid-1" }, http.MethodGet, "/api/artifacts", nil, false},
		{"an upper-case org", func(h *Header) { h.HubOrg = strings.ToUpper(teamOrg) }, http.MethodGet, "/api/artifacts", nil, false},
		{"no stamp, forged headers, owner route", func(h *Header) { h.HubOrg, h.HubTeamRole, h.HubUserID = "", "", "" }, http.MethodGet, "/api/artifacts", forged, false},
		{"complete stamp, forged headers replaced", nil, http.MethodGet, "/api/artifacts", forged, true},
		{"forged headers, other route", nil, http.MethodGet, "/api/health", forged, false},
	} {
		h := full
		if tc.mod != nil {
			tc.mod(&h)
		}
		out := scrub(tc.in, tc.method, tc.path)
		stampTeam(out, h, tc.method, tc.path)
		for k := range out {
			if http.CanonicalHeaderKey(k) != k {
				t.Errorf("%s: non-canonical key %q", tc.name, k)
			}
		}
		if !tc.want {
			assert.Empty(t, out.Values(HubOrgHeader), tc.name)
			assert.Empty(t, out.Values(HubTeamRoleHeader), tc.name)
			assert.Empty(t, out.Values(HubUserIDHeader), tc.name)
			continue
		}
		assert.Equal(t, []string{h.HubOrg}, out.Values(HubOrgHeader), tc.name)
		assert.Equal(t, []string{h.HubTeamRole}, out.Values(HubTeamRoleHeader), tc.name)
		assert.Equal(t, []string{h.HubUserID}, out.Values(HubUserIDHeader), tc.name)
		assert.Equal(t, []string{teamUser}, out.Values(HubUserHeader), tc.name+": the label the person is shown by")
	}
}

// A request's own copy of the three headers is not on the allowlist, in any spelling.
func TestTeamHeadersAreNotForwardedFromARequest(t *testing.T) {
	for _, name := range []string{HubOrgHeader, HubTeamRoleHeader, HubUserIDHeader} {
		assert.False(t, forwardRequestHeaders[name], name)
		in := http.Header{name: {"x"}}
		assert.Empty(t, scrub(in, http.MethodGet, "/api/artifacts").Values(name), name)
	}
}

// End to end: the team stamp reaches the Coral server on an owner route and nowhere else; a browser's own copy never does; the connector's Hello says it is a team's.
func TestTeamStampReachesTheServerOnlyOnOwnerRoutes(t *testing.T) {
	var mu sync.Mutex
	var got []http.Header
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(200)
	})
	hub := newHub(t, fastCfg())
	launch := func() Mode { return TierLaunch }
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.MaxMode = launch
		l.AllowPath = nil
		l.AllowRequest = AllowRequest(launch)
	})
	do := func(method, path string, ctx context.Context, hdr http.Header) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, "http://x"+path, nil)
		for k, vs := range hdr {
			req.Header[k] = vs
		}
		resp, err := sess.RoundTrip(req)
		require.NoError(t, err, "%s %s", method, path)
		resp.Body.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	team := WithHubUser(WithHubTeam(WithHubTier(ctx, "launch"), HubTeam{Org: teamOrg, Role: "admin", UserID: teamUID}), teamUser)
	forged := http.Header{"X-Coral-Hub-Org": {teamOrg}, "x-coral-hub-team-role": {"owner"}, "X-Coral-Hub-User-Id": {teamUID}}
	do(http.MethodGet, "/api/artifacts", team, nil)
	do(http.MethodGet, "/api/health", team, forged)
	do(http.MethodGet, "/api/artifacts", WithHubTier(ctx, "view"), forged)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 3)
	assert.Equal(t, teamOrg, got[0].Get(HubOrgHeader))
	assert.Equal(t, "admin", got[0].Get(HubTeamRoleHeader))
	assert.Equal(t, teamUID, got[0].Get(HubUserIDHeader))
	assert.Equal(t, teamUser, got[0].Get(HubUserHeader))
	assert.Empty(t, got[0].Get(HubRoleHeader), "a team stamp is not the personal owner marker")
	for i, why := range []string{"", "another route", "a stream with no stamp"}[1:] {
		for _, n := range []string{HubOrgHeader, HubTeamRoleHeader, HubUserIDHeader} {
			assert.Empty(t, got[i+1].Values(n), why+": "+n)
		}
	}
}

// The Hello carries owner_kind and the team_owner/1 capability; the hub reads them from the session.
func TestHelloSaysTheOwnerKindAndTheCapability(t *testing.T) {
	assert.Equal(t, OwnerKindPersonal, currentOwnerKind(nil))
	assert.Equal(t, OwnerKindTeam, currentOwnerKind(func() string { return OwnerKindTeam }))
	assert.Equal(t, OwnerKindPersonal, currentOwnerKind(func() string { return "Team" }), "anything but team is personal")
	assert.Equal(t, OwnerKindPersonal, currentOwnerKind(func() string { return "" }))
	assert.Equal(t, "", wellFormedOwnerKind("organisation"))
	assert.Equal(t, "", wellFormedOwnerKind(""))
	assert.Equal(t, OwnerKindTeam, wellFormedOwnerKind("team"))
	assert.True(t, HasCapability([]string{CapTeamOwner}, "team_owner/1"))
}

// End to end: the Hello of a team machine says "team" and team_owner/1, one that says nothing is personal, and the hub reads both from the session.
func TestSessionReadsTheOwnerKindFromTheHello(t *testing.T) {
	dial := func(kind func() string) *Session {
		hub := newHub(t, fastCfg())
		ls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		t.Cleanup(ls.Close)
		cs, err := Dial(context.Background(), hub.URL(), testToken, ClientOptions{
			SessionConfig: hub.Cfg, Handler: &LocalHandler{BaseURL: ls.URL, AllowPath: allowAll}, CoralVersion: "test", AllowInsecure: true, OwnerKind: kind,
		})
		require.NoError(t, err)
		t.Cleanup(func() { cs.Close() })
		return hub.next(t)
	}
	team := dial(func() string { return OwnerKindTeam })
	assert.Equal(t, OwnerKindTeam, team.PeerOwnerKind())
	assert.True(t, team.HasPeerCapability(CapTeamOwner))
	personal := dial(nil)
	assert.Equal(t, OwnerKindPersonal, personal.PeerOwnerKind())
	assert.True(t, personal.HasPeerCapability(CapTeamOwner), "the capability is what this Coral can do, not what it is enrolled as")
}

// Security's LOW on #7061: stampTeam replaces whatever the request headers already hold under the three names (and the hub user), on an owner route with a complete stamp, and removes them on any other
// route or an incomplete stamp: the forward allowlist already drops them, and this pins that the stamp does not depend on it.
func TestStampTeamReplacesPreloadedHeaders(t *testing.T) {
	preload := func() http.Header {
		h := http.Header{}
		h.Set(HubOrgHeader, "ffffffff-0000-4000-8000-000000000001")
		h.Set(HubTeamRoleHeader, "owner")
		h.Add(HubTeamRoleHeader, "admin")
		h.Set(HubUserIDHeader, "hub-uid-ffffffffffffffffffffffffffffffff")
		h.Set(HubUserHeader, "hub-user-ffffffff")
		return h
	}
	full := Header{Kind: KindHTTP, HubUser: teamUser, HubOrg: teamOrg, HubTeamRole: "admin", HubUserID: teamUID}

	out := preload()
	stampTeam(out, full, http.MethodGet, "/api/artifacts")
	assert.Equal(t, []string{teamOrg}, out.Values(HubOrgHeader))
	assert.Equal(t, []string{"admin"}, out.Values(HubTeamRoleHeader), "the two preloaded roles are one, the stream's")
	assert.Equal(t, []string{teamUID}, out.Values(HubUserIDHeader))
	assert.Equal(t, []string{teamUser}, out.Values(HubUserHeader))

	for name, tc := range map[string]struct {
		h    Header
		path string
	}{
		"another route":        {full, "/api/sessions/live"},
		"no org on the stream": {Header{Kind: KindHTTP, HubUser: teamUser, HubTeamRole: "admin", HubUserID: teamUID}, "/api/artifacts"},
		"no id on the stream":  {Header{Kind: KindHTTP, HubUser: teamUser, HubOrg: teamOrg, HubTeamRole: "admin"}, "/api/artifacts"},
	} {
		out := preload()
		stampTeam(out, tc.h, http.MethodGet, tc.path)
		for _, n := range []string{HubOrgHeader, HubTeamRoleHeader, HubUserIDHeader} {
			assert.Empty(t, out.Values(n), name+": "+n)
		}
	}
}
