package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A remote dashboard (the LAN address, or the phone via the QR link) that
// logged in has a session cookie: it may turn auto-accept on like the local
// one. An API key (scripts, a hub's Bearer proxy) and a cross-site page may not.
func TestAutoAcceptEnableFromALoggedInRemoteDashboard(t *testing.T) {
	r := newE2ERemote(t, "autoaccept-lan", nil)
	h := r.srv.Router()
	ctx := context.Background()
	ss := store.NewSessionStore(r.db)
	sid := "11111111-2222-3333-4444-555555555555"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "dev", WorkingDir: r.work}))
	body := `{"session_id":"` + sid + `","acknowledged":true}`
	lan := func(target string, hdr map[string]string, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", target, strings.NewReader(body))
		req.RemoteAddr = "192.168.1.20:51000"
		req.Host = "192.168.1.5:8420"
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "coral_session", Value: cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	enable := "/api/sessions/live/dev/auto-accept/enable"
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	codeOf := func(rec *httptest.ResponseRecorder) string {
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		c, _ := out["code"].(string)
		return c
	}

	// Not logged in: the auth middleware refuses.
	require.Equal(t, 401, lan(enable, same, "").Code)
	// The API key, as a script or a hub's proxy sends it: authenticated, but not a dashboard.
	rec := lan(enable, mergeHdr(same, map[string]string{"Authorization": "Bearer " + r.key()}), "")
	require.Equal(t, 403, rec.Code, rec.Body.String())
	assert.Equal(t, "no_dashboard_provenance", codeOf(rec))
	rec = lan(enable+"?api_key="+r.key(), same, "")
	require.Equal(t, 403, rec.Code, rec.Body.String())

	token, err := r.srv.keyStore.CreateSession("192.168.1.20", "test")
	require.NoError(t, err)
	// A logged-in browser whose URL also carries a credential is a script or a link,
	// not the dashboard: refused for any credential-named parameter (#3279).
	for _, q := range []string{"?token=x", "?apikey=x", "?api_key=x"} {
		rec = lan(enable+q, same, token)
		require.Equal(t, 403, rec.Code, q+" "+rec.Body.String())
		assert.Equal(t, "no_dashboard_provenance", codeOf(rec), q)
	}
	// Logged in, but another site's page: refused.
	rec = lan(enable, map[string]string{"Sec-Fetch-Site": "cross-site"}, token)
	require.Equal(t, 403, rec.Code)
	assert.Equal(t, "no_dashboard_provenance", codeOf(rec))
	rec = lan(enable, map[string]string{"Origin": "http://evil.example"}, token)
	require.Equal(t, 403, rec.Code)
	// A session plus a Bearer key is a proxy, not a browser.
	rec = lan(enable, mergeHdr(same, map[string]string{"Authorization": "Bearer " + r.key()}), token)
	require.Equal(t, 403, rec.Code)
	// The logged-in dashboard: allowed (fetch metadata, or the Origin fallback).
	rec = lan(enable, same, token)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"auto_accept_source":"local"`)
	rec = lan(enable, map[string]string{"Origin": "http://192.168.1.5:8420"}, token)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	// The phone: the QR link carries ?api_key= once, which sets the session
	// cookie; the dashboard's later fetches use the cookie.
	req := httptest.NewRequest("GET", "/?api_key="+r.key(), nil)
	req.RemoteAddr = "192.168.1.30:52000"
	req.Host = "192.168.1.5:8420"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var phone string
	for _, c := range rr.Result().Cookies() {
		if c.Name == "coral_session" {
			phone = c.Value
		}
	}
	require.NotEmpty(t, phone, "the QR link logs the phone in")
	rec = lan(enable, same, phone)
	require.Equal(t, 200, rec.Code, rec.Body.String())
}
