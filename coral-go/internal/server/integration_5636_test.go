package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5636 through the real connector and the real router: the Coral Hub owner's read of one team's cross-team state answers the owner and nobody else, and says nothing of any other team.
func TestTheOwnerReadOfATeamsCrossTeamStateThroughTheRealConnector(t *testing.T) {
	g := newInteg(t)
	team, other := g.ids["alice-team"], g.ids["bob-team"]
	path := "/api/board/" + team + "/cross-team/settings"

	code, body := g.hub("owner", integOwner, "POST", "/api/board/"+team+"/cross-team/enable", "{}", nil)
	require.Equal(t, http.StatusOK, code, body)

	code, body = g.hub("owner", integOwner, "GET", path, "", nil)
	require.Equal(t, http.StatusOK, code, body)
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	keys := []string{}
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"cross_team_artifacts", "cross_team_messaging", "hub_may_change", "last_changed", "linkable"}, keys)
	assert.Equal(t, true, out["cross_team_messaging"])
	assert.Equal(t, true, out["cross_team_artifacts"], "files were never set: the default, on (#5916)")
	assert.NotContains(t, body, other, "no id of another team")
	assert.NotContains(t, body, "bob", "nothing of another team's agents or labels")

	forged := map[string]string{tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: integOwner, tunnel.ViaHeader: cloud.ViaSecret()}
	for name, c := range map[string]struct {
		role, user string
		hdr        map[string]string
	}{
		"a grantee": {"", integGrantee, nil}, "another hub user holding the role": {"owner", integGrantee, nil}, "the owner's user without the role": {"", integOwner, nil},
		"a forged marker on a grantee": {"", integGrantee, forged}, "no hub user": {"owner", "", nil}, "the owner role in capitals": {"OWNER", integOwner, nil},
	} {
		// a real team and one that does not exist answer alike: no oracle for a non-owner
		for _, p := range []string{path, "/api/board/b-00000000000000000000000000000000/cross-team/settings"} {
			code, body := g.hub(c.role, c.user, "GET", p, "", c.hdr)
			assert.Equal(t, http.StatusForbidden, code, "%s %s: %s", name, p, body)
			assert.NotContains(t, body, "cross_team_messaging", name)
		}
	}
	for _, a := range []string{"alice-dev", "bob-dev", "ops-dev"} {
		code, body := g.agent(a, "GET", path, "", nil)
		assert.Equal(t, http.StatusForbidden, code, "an agent (%s): %s", a, body)
		assert.NotContains(t, body, "cross_team_messaging", a)
	}
	// this computer, with no marker and then with a forged one (the stamp is the connector's only), is refused too
	code, body = g.agent("", "GET", path, "", nil)
	assert.Contains(t, []int{http.StatusForbidden, http.StatusUnauthorized}, code, body)

	code, _ = g.hub("owner", integOwner, "GET", "/api/board/b-00000000000000000000000000000000/cross-team/settings", "", nil)
	assert.Equal(t, http.StatusNotFound, code, "the owner is told a team does not exist")
}
