package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// Auto-accept through the Cloud Hub (#3182): enable is Launch only, disable and
// disable-all are Interact, View refuses all three, and the owner's local
// switch is never reachable through the tunnel.
func TestAutoAcceptRouteTiers(t *testing.T) {
	enable, disable, all := "/api/sessions/live/dev/auto-accept/enable", "/api/sessions/live/dev/auto-accept/disable", "/api/auto-accept/disable-all"
	require.Equal(t, tunnel.TierLaunch, tunnel.Classify("POST", enable, ""))
	require.Equal(t, tunnel.TierInteract, tunnel.Classify("POST", disable, ""))
	require.Equal(t, tunnel.TierInteract, tunnel.Classify("POST", all, ""))
	require.Equal(t, tunnel.TierNever, tunnel.Classify("PUT", "/api/cloud/allow-auto-accept", ""))
	require.Equal(t, tunnel.TierNever, tunnel.Classify("PUT", "/api/sessions/live/dev/auto-accept", ""), "the v1 toggle is gone")

	// The connector's own check, per local maximum mode.
	for _, tc := range []struct {
		mode                    tunnel.Mode
		enable, disable, allOff bool
	}{
		{tunnel.TierView, false, false, false},
		{tunnel.TierInteract, false, true, true},
		{tunnel.TierLaunch, true, true, true},
	} {
		allow := tunnel.AllowRequest(func() tunnel.Mode { return tc.mode })
		assert.Equal(t, tc.enable, allow("POST", enable, ""), "%v enable", tc.mode)
		assert.Equal(t, tc.disable, allow("POST", disable, ""), "%v disable", tc.mode)
		assert.Equal(t, tc.allOff, allow("POST", all, ""), "%v disable-all", tc.mode)
		assert.False(t, allow("PUT", "/api/cloud/allow-auto-accept", ""), "%v allow switch", tc.mode)
	}

	// The server-side guard, through the real router, with the connector's real stamp.
	r := newE2ERemote(t, "autoaccept-tiers", nil)
	h := r.srv.Router()
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), "Content-Type": "application/json"}
	js := map[string]string{"Content-Type": "application/json"}
	setMode := func(m string) {
		require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"`+m+`"}`, js).Code)
	}
	code := func(path string) int {
		return call(h, "POST", path, `{"session_id":"x","acknowledged":true}`, hub).Code
	}

	setMode("view")
	assert.Equal(t, 403, code(enable))
	assert.Equal(t, 403, code(disable))
	assert.Equal(t, 403, code(all))
	setMode("interact")
	assert.Equal(t, 403, code(enable))
	assert.NotEqual(t, 403, code(disable))
	assert.Equal(t, 200, code(all))
	setMode("launch")
	assert.NotEqual(t, 403, code(enable), "Launch reaches the handler (404: no such session)")
	for _, m := range []string{"view", "interact", "launch"} {
		setMode(m)
		assert.Equal(t, 403, call(h, "PUT", "/api/cloud/allow-auto-accept", `{"allow":true}`, hub).Code, m)
	}
}

// A forged stamp never passes: curl with X-Coral-Via-Tunnel and no browser
// fetch metadata is refused as having no dashboard provenance, and so is a
// stamp added to a same-origin request.
func TestAutoAcceptForgedTunnelStampIsRefused(t *testing.T) {
	r := newE2ERemote(t, "autoaccept-forged", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)
	ss := store.NewSessionStore(r.db)
	sid := "11111111-2222-3333-4444-555555555555"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "dev", WorkingDir: r.work}))
	body := `{"session_id":"` + sid + `","acknowledged":true}`
	for _, hdr := range []map[string]string{
		{tunnel.ViaHeader: "1", "Sec-Fetch-Site": ""},                     // curl with a forged stamp
		{tunnel.ViaHeader: "1"},                                           // a forged stamp on a same-origin request
		{tunnel.ViaHeader: cloud.ViaSecret() + "x", "Sec-Fetch-Site": ""}, // close is not equal
	} {
		rec := call(h, "POST", "/api/sessions/live/dev/auto-accept/enable", body, mergeHdr(js, hdr))
		require.Equal(t, 403, rec.Code, "%v: %s", hdr, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		assert.Equal(t, "no_dashboard_provenance", out["code"], "%v", hdr)
	}
	st, err := ss.GetAutoAcceptState(context.Background(), sid)
	require.NoError(t, err)
	assert.False(t, st.Enabled)

	// The real stamp is the hub path; the dashboard is the local path.
	rec := call(h, "POST", "/api/sessions/live/dev/auto-accept/enable", body, mergeHdr(js, map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), "Sec-Fetch-Site": ""}))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"auto_accept_source":"hub"`)
	rec = call(h, "POST", "/api/sessions/live/dev/auto-accept/enable", body, js)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"auto_accept_source":"local"`)
}

// The owner's switch "Cloud Hub can turn on auto-accept" is on by default, set
// only from the local dashboard, and turning it off ends the hub's enables (not
// local ones). Lowering the access mode below Launch ends them too.
func TestAutoAcceptHubAllowSwitchAndModeDrop(t *testing.T) {
	r := newE2ERemote(t, "autoaccept-allow", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	ctx := context.Background()
	ss := store.NewSessionStore(r.db)

	status := func() map[string]any {
		rec := call(h, "GET", "/api/cloud/status", "", nil)
		require.Equal(t, 200, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	assert.Equal(t, true, status()["allow_hub_auto_accept"], "on by default")

	sids := map[string]string{"hub": "11111111-2222-3333-4444-555555555555", "local": "22222222-2222-3333-4444-555555555555"}
	seed := func() {
		for src, sid := range sids {
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: src, WorkingDir: r.work}))
			_, err := ss.EnableAutoAccept(ctx, sid, src, 0)
			require.NoError(t, err)
		}
	}
	state := func(src string) store.AutoAcceptState {
		st, err := ss.GetAutoAcceptState(ctx, sids[src])
		require.NoError(t, err)
		return st
	}

	seed()
	require.Equal(t, 400, call(h, "PUT", "/api/cloud/allow-auto-accept", `{}`, js).Code, "allow is required")
	require.Equal(t, 403, call(h, "PUT", "/api/cloud/allow-auto-accept", `{"allow":false}`, mergeHdr(js, map[string]string{"Sec-Fetch-Site": ""})).Code, "local dashboard only")
	assert.True(t, state("hub").Enabled)
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/allow-auto-accept", `{"allow":false}`, js).Code)
	assert.Equal(t, false, status()["allow_hub_auto_accept"])
	assert.False(t, state("hub").Enabled)
	assert.Equal(t, "hub_allow_off", state("hub").PausedCode.String)
	assert.True(t, state("local").Enabled, "a local enable is the owner's own")
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/allow-auto-accept", `{"allow":true}`, js).Code)
	assert.Equal(t, true, status()["allow_hub_auto_accept"])

	// Access lowered below Launch.
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)
	seed()
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)
	assert.True(t, state("hub").Enabled, "staying at Launch changes nothing")
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"interact"}`, js).Code)
	assert.False(t, state("hub").Enabled)
	assert.Equal(t, "mode_lowered", state("hub").PausedCode.String)
	assert.True(t, state("local").Enabled)
}
