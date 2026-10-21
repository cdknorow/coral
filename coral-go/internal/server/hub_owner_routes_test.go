package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5307 through the real router, with the connector's real stamp: the owner's reads and saves of this computer's configuration work at launch
// tier and not below it; a grantee at launch tier is refused by the machine; the enrolled label is checked.
func TestTheOwnerRoutesThroughTheHubOnTheRealRouter(t *testing.T) {
	r := newE2ERemote(t, "owner-routes", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	ctx := t.Context()

	r.srv.cloudHandler.SetLinkedCheck(func() bool { return true }) // a linked computer: the Hub may act as the Operator unless the owner opted out
	cs := store.NewCloudStore(r.db, store.NewRemoteServerStore(r.db, r.cfg.CoralDir()))
	require.NoError(t, cs.Save(ctx, "https://hub.example.invalid", "srv_1", "L", "coralhub_srv_abcd1234_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))
	pinned, err := cs.PinOwnerUser(ctx, "srv_1", "hub-user-1234abcd")
	require.NoError(t, err)
	require.True(t, pinned)

	owner := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: "hub-user-1234abcd", "Content-Type": "application/json"}
	grantee := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: "hub-user-9999ffff", "Content-Type": "application/json"}
	otherOwner := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: "hub-user-9999ffff", "Content-Type": "application/json"}
	const health = `{"subscriber_id":"Operator","health":{"on":false}}`

	// View mode (the default cap): refused before any handler runs, owner or not.
	assert.Equal(t, 403, call(h, "GET", "/api/settings/prompt-inspection", "", owner).Code)
	assert.Equal(t, 403, call(h, "PUT", "/api/board/team/settings", health, owner).Code)

	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)
	assert.Equal(t, 200, call(h, "GET", "/api/settings/prompt-inspection?board=team", "", owner).Code, "the owner reads the prompts")
	assert.Equal(t, 403, call(h, "GET", "/api/settings/prompt-inspection", "", grantee).Code, "a grantee at launch tier does not")
	assert.Equal(t, 403, call(h, "GET", "/api/settings/prompt-inspection", "", otherOwner).Code, "a marker with another label does not")
	assert.Equal(t, 403, call(h, "GET", "/api/settings/prompt-inspection?x=1", "", owner).Code, "only the board query passes the tunnel")

	assert.Equal(t, 403, call(h, "PUT", "/api/board/team/settings", health, grantee).Code)
	assert.Equal(t, 403, call(h, "PUT", "/api/board/team/settings", health, otherOwner).Code)
	assert.Equal(t, 403, call(h, "PUT", "/api/board/team/settings", `{"subscriber_id":"Operator","cross_team_messaging":true}`, owner).Code, "cross-team stays local")
	rec := call(h, "PUT", "/api/board/team/settings", health, owner)
	assert.Equal(t, 200, rec.Code, "the owner saves the health setup: %s", rec.Body.String())
	// And an agent's own routes are untouched: the hub still has no PUT /api/settings.
	assert.Equal(t, 403, call(h, "PUT", "/api/settings", `{"theme":"dark"}`, owner).Code)
}
