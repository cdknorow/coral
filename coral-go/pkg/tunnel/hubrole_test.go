package tunnel

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsUpdateRouteIsExactlyTheTwoRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/system/update", true},
		{http.MethodPost, "/api/system/update/relaunch", true},
		{http.MethodPost, "/api/system/update", false},
		{http.MethodGet, "/api/system/update/relaunch", false},
		{http.MethodPut, "/api/system/update/relaunch", false},
		{http.MethodGet, "/api/system/update-check", false},
		{http.MethodGet, "/api/system/update/", false},
		{http.MethodPost, "/api/system/update/relaunch/x", false},
		{http.MethodGet, "/api/system/hub-operator", false},
	} {
		assert.Equal(t, tc.want, IsUpdateRoute(tc.method, tc.path), "%s %s", tc.method, tc.path)
	}
}

const hexID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestStampRoleOnlyForAnOwnerStreamOnAnUpdateRoute(t *testing.T) {
	const user = "hub-user-1234abcd"
	for _, tc := range []struct {
		name         string
		role         string
		method, path string
		inHeaders    http.Header
		wantRole     string
		wantUser     string
	}{
		{"owner on the relaunch", "owner", http.MethodPost, "/api/system/update/relaunch", nil, "owner", user},
		{"owner on the status", "owner", http.MethodGet, "/api/system/update", nil, "owner", user},
		{"owner elsewhere", "owner", http.MethodGet, "/api/sessions/live", nil, "", ""},
		{"owner on a board write", "owner", http.MethodPost, "/api/board/t/messages", nil, "", ""},
		{"no role on the relaunch", "", http.MethodPost, "/api/system/update/relaunch", nil, "", ""},
		{"a request's own marker, no role", "", http.MethodPost, "/api/system/update/relaunch", http.Header{HubRoleHeader: {"owner"}}, "", ""},
		{"a request's own marker, other route", "owner", http.MethodGet, "/api/health", http.Header{HubRoleHeader: {"owner"}}, "", ""},
		{"two spellings of the marker", "", http.MethodPost, "/api/system/update/relaunch", http.Header{"x-coral-hub-role": {"owner"}, "X-CORAL-HUB-ROLE": {"owner"}}, "", ""},
		{"a request's own user, owner stream", "owner", http.MethodGet, "/api/system/update", http.Header{HubUserHeader: {"hub-user-ffffffff"}}, "owner", user},
		{"owner on an artifact read", "owner", http.MethodGet, "/api/artifacts/" + hexID, nil, "owner", user},
		{"owner on the artifact list", "owner", http.MethodGet, "/api/artifacts", nil, "owner", user},
		{"owner deleting an artifact", "owner", http.MethodDelete, "/api/artifacts/" + hexID, nil, "owner", user},
		{"a grantee on an artifact read", "", http.MethodGet, "/api/artifacts/" + hexID, nil, "", ""},
		{"a request's own marker on an artifact read, no role", "", http.MethodGet, "/api/artifacts/" + hexID, http.Header{HubRoleHeader: {"owner"}}, "", ""},
	} {
		h := Header{Kind: KindHTTP, Method: tc.method, Path: tc.path, HubUser: user, HubRole: tc.role}
		out := scrub(tc.inHeaders, tc.method, tc.path)
		stampRole(out, h, tc.method, tc.path)
		assert.Equal(t, tc.wantRole, out.Get(HubRoleHeader), tc.name)
		// A forged spelling never survives, whatever the role.
		for k := range out {
			if http.CanonicalHeaderKey(k) != k {
				t.Errorf("%s: non-canonical key %q", tc.name, k)
			}
		}
		assert.Len(t, out.Values(HubRoleHeader), map[bool]int{true: 1, false: 0}[tc.wantRole != ""], tc.name)
		if tc.wantUser != "" {
			assert.Equal(t, tc.wantUser, out.Get(HubUserHeader), tc.name)
		}
	}
}

func TestHubRoleOnlyOwnerSurvivesValidationAndContext(t *testing.T) {
	assert.Equal(t, "owner", hubRoleFrom(WithHubRole(context.Background(), "owner")))
	for _, bad := range []string{"", "Owner", "admin", "owner ", "view"} {
		assert.Empty(t, hubRoleFrom(WithHubRole(context.Background(), bad)), "%q", bad)
	}
	l := Limits{}.withDefaults()
	ok := Header{Kind: KindHTTP, Method: "GET", Path: "/x", HubRole: "owner"}
	assert.NoError(t, ok.validate(l))
	bad := ok
	bad.HubRole = "admin"
	assert.ErrorIs(t, bad.validate(l), ErrBadHeader)
}

// End to end: the owner stream's marker reaches the Coral server on the two routes and nowhere else, and a browser's own copy
// of the header (any spelling) never does.
func TestHubRoleReachesTheServerOnlyOnTheUpdateRoutes(t *testing.T) {
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
	owner := WithHubUser(WithHubRole(WithHubTier(ctx, "launch"), "owner"), "hub-user-1234abcd")
	forged := http.Header{"X-Coral-Hub-Role": {"owner"}, "x-coral-hub-role": {"owner"}}

	do(http.MethodGet, "/api/system/update", owner, nil)
	do(http.MethodPost, "/api/system/update/relaunch", owner, nil)
	do(http.MethodGet, "/api/health", owner, forged)
	do(http.MethodGet, "/api/system/update", WithHubTier(ctx, "view"), forged)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 4)
	assert.Equal(t, "owner", got[0].Get(HubRoleHeader))
	assert.Equal(t, "hub-user-1234abcd", got[0].Get(HubUserHeader))
	assert.Equal(t, "owner", got[1].Get(HubRoleHeader))
	assert.Empty(t, got[2].Get(HubRoleHeader), "another route: no marker, the browser's copy dropped")
	assert.Empty(t, got[3].Get(HubRoleHeader), "a stream with no role: the browser's copy dropped on the update route too")
	assert.Empty(t, got[3].Get(HubUserHeader))
}

func TestIsOwnerRouteIsTheUpdateRoutesAndTheOwnersConfiguration(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/system/update", true},
		{http.MethodPost, "/api/system/update/relaunch", true},
		{http.MethodGet, "/api/board/team/settings", true},
		{http.MethodPut, "/api/board/team/settings", true},
		{http.MethodGet, "/api/settings/prompt-inspection", true},
		{http.MethodPost, "/api/board/team/settings", false},
		{http.MethodDelete, "/api/board/team/settings", false},
		{http.MethodPut, "/api/settings/prompt-inspection", false},
		{http.MethodGet, "/api/board/team/settings/x", false},
		{http.MethodGet, "/api/board//settings", false},
		{http.MethodGet, "/api/board/a/b/settings", false},
		{http.MethodGet, "/api/settings", false},
		{http.MethodPut, "/api/settings", false},
		{http.MethodGet, "/api/settings/default-prompts", false},
		{http.MethodGet, "/api/board/team/status", false},
		// the artifact routes (#5575, #5577): the machine answers them to the owner alone, so the marker must be stamped on them
		{http.MethodGet, "/api/artifacts", true},
		{http.MethodGet, "/api/artifacts/" + hexID, true},
		{http.MethodGet, "/api/artifacts/" + hexID + "/meta", true},
		{http.MethodGet, "/api/artifacts/" + hexID + "/comments", true},
		{http.MethodGet, "/api/artifacts/" + hexID + "/readers", true},
		{http.MethodDelete, "/api/artifacts/" + hexID, true},
		{http.MethodGet, "/api/board/team/artifacts", true},
		{http.MethodGet, "/api/board/team/tasks/12/artifact-content", true},
		{http.MethodGet, "/api/session-artifacts", true},
		{http.MethodPost, "/api/artifacts/" + hexID, false},
		{http.MethodPut, "/api/artifacts/" + hexID, false},
		{http.MethodDelete, "/api/artifacts", false},
		{http.MethodGet, "/api/artifacts/" + hexID[:63], false},
		{http.MethodGet, "/api/artifacts/" + strings.ToUpper(hexID), false},
		{http.MethodGet, "/api/artifacts/" + hexID + "/keep", false},
		{http.MethodGet, "/api/artifacts/" + hexID + "/x", false},
		{http.MethodGet, "/api/board/team/artifacts/x", false},
		{http.MethodGet, "/api/board/a/b/artifacts", false},
		{http.MethodGet, "/api/board/team/tasks/x/artifact-content", true}, // the table's {id} is any one segment (the handler refuses a non-number); the old hand-written list wanted digits: the one difference of #5610
		// Coral Hub's owner turns a team's cross-team switches on or off (#5572): the four POST routes only.
		{http.MethodPost, "/api/board/team/cross-team/enable", true},
		{http.MethodPost, "/api/board/team/cross-team/disable", true},
		{http.MethodPost, "/api/board/team/cross-team/files/enable", true},
		{http.MethodPost, "/api/board/team/cross-team/files/disable", true},
		{http.MethodGet, "/api/board/team/cross-team/enable", false},
		{http.MethodPut, "/api/board/team/cross-team/enable", false},
		{http.MethodPost, "/api/board/team/cross-team/toggle", false},
		{http.MethodPost, "/api/board/team/cross-team/files", false},
		{http.MethodPost, "/api/board//cross-team/enable", false},
		{http.MethodPost, "/api/board/a/b/cross-team/enable", false},
		{http.MethodPost, "/api/board/team/cross-team/enable/x", false},
		{http.MethodPut, "/api/cloud/hub-cross-team", false},
	} {
		assert.Equal(t, tc.want, IsOwnerRoute(tc.method, tc.path), "%s %s", tc.method, tc.path)
	}
}

func TestTheOwnerMarkerReachesTheConfigurationRoutesOnlyForAnOwnerStream(t *testing.T) {
	var mu sync.Mutex
	type seen struct{ method, path, role, user string }
	var got []seen
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, seen{r.Method, r.URL.Path, r.Header.Get(HubRoleHeader), r.Header.Get(HubUserHeader)})
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	owner := WithHubUser(WithHubRole(WithHubTier(ctx, "launch"), "owner"), "hub-user-1234abcd")
	grantee := WithHubUser(WithHubTier(ctx, "launch"), "hub-user-9999ffff")
	forged := http.Header{"X-Coral-Hub-Role": {"owner"}, "x-coral-hub-role": {"owner"}}
	do := func(c context.Context, method, path string, hdr http.Header) {
		t.Helper()
		req, _ := http.NewRequestWithContext(c, method, "http://x"+path, strings.NewReader("{}"))
		for k, vs := range hdr {
			req.Header[k] = vs
		}
		resp, err := sess.RoundTrip(req)
		require.NoError(t, err, "%s %s", method, path)
		resp.Body.Close()
	}
	do(owner, http.MethodGet, "/api/board/team/settings", nil)
	do(owner, http.MethodPut, "/api/board/team/settings", nil)
	do(owner, http.MethodGet, "/api/settings/prompt-inspection?board=team", nil)
	do(grantee, http.MethodPut, "/api/board/team/settings", forged)
	do(grantee, http.MethodGet, "/api/settings/prompt-inspection", forged)
	do(owner, http.MethodGet, "/api/board/team/status", forged) // not an owner route
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 6)
	for i := 0; i < 3; i++ {
		assert.Equal(t, "owner", got[i].role, "%s %s", got[i].method, got[i].path)
		assert.Equal(t, "hub-user-1234abcd", got[i].user)
	}
	assert.Empty(t, got[3].role, "a grantee's stream: the browser's copy is dropped")
	assert.Empty(t, got[4].role)
	assert.Empty(t, got[5].role, "another route: no marker even for an owner stream")
}

// #6088: the image upload's hub account comes from the stream's own HubUser field and from nothing the request carried: a header from the browser or the hub is replaced by it or removed.
func TestStampUploaderTakesTheAccountFromTheStreamOnly(t *testing.T) {
	const acct, forged = "hub-user-0a1b2c3d", "hub-user-ffffffff"
	for _, tc := range []struct {
		name         string
		method, path string
		stream       string // the stream's HubUser
		carried      string // a header the request itself carried
		want         string
	}{
		{"the stream's account", http.MethodPost, UploadPath, acct, "", acct},
		{"a carried header is replaced", http.MethodPost, UploadPath, acct, forged, acct},
		{"a carried header with no account on the stream is removed", http.MethodPost, UploadPath, "", forged, ""},
		{"an account that is not a hub user id is not stamped", http.MethodPost, UploadPath, "alice@example.com", forged, ""},
		{"a too-long value is not stamped", http.MethodPost, UploadPath, "hub-user-0a1b2c3d00", "", ""},
		{"another route is left alone", http.MethodPost, "/api/upload/shot.png", acct, "", ""},
		{"another method is left alone", http.MethodGet, UploadPath, acct, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := http.Header{}
			if tc.carried != "" {
				out.Set(HubUserHeader, tc.carried)
			}
			stampUploader(out, Header{HubUser: tc.stream}, tc.method, tc.path)
			if tc.method == http.MethodPost && tc.path == UploadPath {
				assert.Equal(t, tc.want, out.Get(HubUserHeader))
			} else {
				assert.Equal(t, tc.carried, out.Get(HubUserHeader), "untouched")
			}
		})
	}
}

// #6088, end to end through a real session: the hub account reaches the server's upload handler from the stream's field alone; a forged header, in any spelling, never does; a stream with no
// account sends none; and no other route gets it.
func TestTheUploadsHubAccountReachesTheServerFromTheStreamOnly(t *testing.T) {
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
	user := WithHubUser(WithHubTier(ctx, "launch"), "hub-user-0a1b2c3d")
	forged := http.Header{"X-Coral-Hub-User": {"hub-user-ffffffff"}, "x-coral-hub-user": {"hub-user-eeeeeeee"}}

	do(http.MethodPost, UploadPath, user, nil)
	do(http.MethodPost, UploadPath, user, forged)
	do(http.MethodPost, UploadPath, WithHubTier(ctx, "launch"), forged)
	do(http.MethodGet, "/api/health", user, forged)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 4)
	assert.Equal(t, []string{"hub-user-0a1b2c3d"}, got[0].Values(HubUserHeader))
	assert.Equal(t, []string{"hub-user-0a1b2c3d"}, got[1].Values(HubUserHeader), "the stream's account replaces any the request carried")
	assert.Empty(t, got[2].Values(HubUserHeader), "no account on the stream: none, and the carried copies are gone")
	assert.Empty(t, got[3].Values(HubUserHeader), "another route: the header is dropped")
}
