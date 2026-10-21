package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5572: the four routes on which Coral Hub's owner turns a team's cross-team switches on or off are Launch tier and marked as owner routes; the lock that governs them is written only on this
// computer (its own route; never through the tunnel in any role; not by PUT /api/settings), and with it off the Hub is refused in both directions, end to end through the real router.
func TestTheHubOwnerSwitchRoutesAndTheLockThroughTheRealRouter(t *testing.T) {
	for _, p := range []string{"cross-team/enable", "cross-team/disable", "cross-team/files/enable", "cross-team/files/disable"} {
		path := "/api/board/team/" + p
		assert.Equal(t, tunnel.TierLaunch, tunnel.Classify("POST", path, ""), p)
		assert.True(t, tunnel.IsOwnerRoute("POST", path), p)
		for _, m := range []string{"GET", "PUT", "PATCH", "DELETE"} {
			assert.Equal(t, tunnel.TierNever, tunnel.Classify(m, path, ""), m+" "+p)
			assert.False(t, tunnel.IsOwnerRoute(m, path), m+" "+p)
		}
	}
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("PUT", "/api/cloud/hub-cross-team", ""))
	assert.False(t, tunnel.IsOwnerRoute("PUT", "/api/cloud/hub-cross-team"), "the lock is never an owner route")
	assert.False(t, tunnel.IsOwnerRoute("POST", "/api/board/team/cross-team/other"))
	assert.False(t, tunnel.IsOwnerRoute("POST", "/api/board/team/x/cross-team/enable"))

	r := newE2ERemote(t, "cross-team-switch", nil)
	h := r.srv.Router()
	routes.SetHubOwnerLabel(func(context.Context) (string, error) { return "hub-user-1234abcd", nil })
	t.Cleanup(func() { routes.SetHubOwnerLabel(nil) })
	js := map[string]string{"Content-Type": "application/json"}
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: "hub-user-1234abcd", "Content-Type": "application/json"}
	grantee := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: "hub-user-9999ffff", "Content-Type": "application/json"}

	// The owner here lets the Hub as far as Launch (the tier of these routes), as the relaunch route needs.
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)
	// A team (the board's first subscriber makes it).
	require.Equal(t, 200, call(h, "POST", "/api/board/team/subscribe", `{"subscriber_id":"Orchestrator","session_name":"s1","role":"Orchestrator","receive_mode":"mentions"}`, js).Code)

	state := func() string {
		s, err := store.NewSessionStore(r.db).GetSettings(t.Context())
		require.NoError(t, err)
		return s["hub_cross_team_settings"]
	}
	assert.Equal(t, "", state(), "nothing stored: the default is on, from the settings registry")

	// Below Launch the route is not offered at all (the mode cap is the router's, before the handler).
	// The default is on: the owner turns the switch on; a grantee cannot.
	assert.Equal(t, 403, call(h, "POST", "/api/board/team/cross-team/enable", "", grantee).Code)
	assert.Equal(t, 200, call(h, "POST", "/api/board/team/cross-team/enable", "", hub).Code)
	assert.Equal(t, 200, call(h, "POST", "/api/board/team/cross-team/files/enable", "", hub).Code)

	// The lock is the owner's on this computer: off refuses the Hub both ways.
	assert.Equal(t, 200, call(h, "PUT", "/api/cloud/hub-cross-team", `{"allow":false}`, js).Code)
	assert.Equal(t, "false", state())
	assert.Equal(t, 403, call(h, "POST", "/api/board/team/cross-team/disable", "", hub).Code, "locked: OFF is refused too")
	assert.Equal(t, 403, call(h, "POST", "/api/board/team/cross-team/files/disable", "", hub).Code)

	// Through the tunnel nobody writes the lock, the owner included, by its route or by PUT /api/settings.
	for name, req := range map[string][3]string{
		"its own route":     {"PUT", "/api/cloud/hub-cross-team", `{"allow":true}`},
		"PUT /api/settings": {"PUT", "/api/settings", `{"hub_cross_team_settings":"true"}`},
	} {
		for who, hdr := range map[string]map[string]string{"owner": hub, "grantee": grantee} {
			assert.Equal(t, 403, call(h, req[0], req[1], req[2], hdr).Code, name+" "+who)
			assert.Equal(t, "false", state(), name+" "+who+": unchanged")
		}
	}
	// The local generic PUT refuses the key too (it has its own route); the route turns it back on.
	assert.Equal(t, 400, call(h, "PUT", "/api/settings", `{"hub_cross_team_settings":"true"}`, js).Code)
	assert.Equal(t, 200, call(h, "PUT", "/api/cloud/hub-cross-team", `{"allow":true}`, js).Code)
	assert.Equal(t, 200, call(h, "POST", "/api/board/team/cross-team/disable", "", hub).Code, "unlocked: it works")

	// The cloud status carries the lock for the Settings page.
	rec := call(h, "GET", "/api/cloud/status", "", js)
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"hub_cross_team_settings":true`)
}
