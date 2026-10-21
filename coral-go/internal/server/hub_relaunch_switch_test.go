package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5280: the mode cap and the relaunch switch are written only on this computer. Through the tunnel, by the switch's own route or
// by PUT /api/settings, with a real connector stamp and the owner marker, each is refused and the stored value is unchanged.
func TestTunneledWritesOfTheCapAndTheRelaunchSwitchAreRefusedAndChangeNothing(t *testing.T) {
	require.Equal(t, tunnel.TierNever, tunnel.Classify("PUT", "/api/cloud/hub-relaunch", ""))
	require.Equal(t, tunnel.TierNever, tunnel.Classify("PUT", "/api/settings", ""))
	require.Equal(t, tunnel.TierView, tunnel.Classify("GET", "/api/system/update", ""))
	require.Equal(t, tunnel.TierLaunch, tunnel.Classify("POST", "/api/system/update/relaunch", ""))
	for _, m := range []string{"PUT", "DELETE", "PATCH"} {
		assert.Equal(t, tunnel.TierNever, tunnel.Classify(m, "/api/system/update/relaunch", ""), m)
		assert.Equal(t, tunnel.TierNever, tunnel.Classify(m, "/api/system/update", ""), m)
	}
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("POST", "/api/system/update", ""))
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("GET", "/api/system/update/relaunch", ""))

	r := newE2ERemote(t, "relaunch-switch", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: "hub-user-1234abcd", "Content-Type": "application/json"}

	// The owner, locally, turns the switch off and sets the cap.
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/hub-relaunch", `{"allow":false}`, js).Code)
	state := func() (string, string) {
		s, err := store.NewSessionStore(r.db).GetSettings(t.Context())
		require.NoError(t, err)
		return s["hub_relaunch_updates"], s["cloud_max_mode"]
	}
	sw, mode := state()
	require.Equal(t, "false", sw)
	require.Equal(t, "launch", mode)

	for name, req := range map[string][3]string{
		"the switch's own route":        {"PUT", "/api/cloud/hub-relaunch", `{"allow":true}`},
		"the cap's own route":           {"PUT", "/api/cloud/max-mode", `{"mode":"view"}`},
		"PUT /api/settings, the switch": {"PUT", "/api/settings", `{"hub_relaunch_updates":"true"}`},
		"PUT /api/settings, the cap":    {"PUT", "/api/settings", `{"cloud_max_mode":"view"}`},
	} {
		assert.Equal(t, 403, call(h, req[0], req[1], req[2], hub).Code, name)
		sw, mode := state()
		assert.Equal(t, "false", sw, name+": the switch is unchanged")
		assert.Equal(t, "launch", mode, name+": the cap is unchanged")
	}
	// The generic PUT refuses both keys for the local owner too (they have their own routes).
	assert.Equal(t, 400, call(h, "PUT", "/api/settings", `{"hub_relaunch_updates":"true"}`, js).Code)
	assert.Equal(t, 400, call(h, "PUT", "/api/settings", `{"cloud_max_mode":"view"}`, js).Code)
}
