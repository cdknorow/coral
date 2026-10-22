package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// "What may Coral Hub do on this computer?" is answered in the Connect call itself: POST /api/cloud/connect takes an optional closed body {"mode", "allow_auto_accept"}, saved before the
// enrollment starts and not at all when any part is invalid; status says whether the cap was ever saved (max_mode_saved); the route stays local only, so Coral Hub cannot pick its own mode.
func TestConnectTakesTheAnswerToWhatCoralHubMayDo(t *testing.T) {
	r := newE2ERemote(t, "connect-answer", nil)
	h := r.srv.Router()
	r.srv.cloudHandler.SetPeerCheck(func(*http.Request) (bool, bool) { return true, true })
	dash := map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8420"}
	do := func(method, path, body string, hdr map[string]string, timeout time.Duration) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr, req.Host = "127.0.0.1:50000", "127.0.0.1:8420"
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		if timeout > 0 { // a connector that was never run waits for its stored enrollment: the answer is saved by then, which is what is checked
			ctx, cancel := context.WithTimeout(req.Context(), timeout)
			t.Cleanup(cancel)
			req = req.WithContext(ctx)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	status := func() map[string]any {
		rec := do("GET", "/api/cloud/status", "", dash, 0)
		require.Equal(t, http.StatusOK, rec.Code)
		var st map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
		return st
	}

	// No hub to connect to: refused, and nothing is saved.
	r.srv.SetCloud(cloud.New(cloud.Options{}))
	rec := do("POST", "/api/cloud/connect", `{"mode":"launch"}`, dash, 0)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, false, status()["max_mode_saved"], "a server with no hub saved nothing")

	r.srv.SetCloud(cloud.New(cloud.Options{HubURL: "https://hub.example.invalid", Store: store.NewCloudStore(r.db, store.NewRemoteServerStore(r.db, r.dir)), MaxMode: func() tunnel.Mode { return tunnel.TierView }}))
	assert.Equal(t, false, status()["max_mode_saved"], "never chosen: an existing install and a new one differ by this")

	// Invalid in any part: 400, nothing saved.
	for name, body := range map[string]string{
		"a mode that is not one":   `{"mode":"admin"}`,
		"an unknown field":         `{"mode":"launch","allow_hub":true}`,
		"a wrong type":             `{"mode":"launch","allow_auto_accept":"yes"}`,
		"a second value":           `{"mode":"launch"} {"mode":"view"}`,
		"a mode of the wrong kind": `{"mode":3}`,
	} {
		rec := do("POST", "/api/cloud/connect", body, dash, 0)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.Equal(t, false, status()["max_mode_saved"], name+": nothing was saved")
	}

	// The command line's plain connect (no body, or {}) saves nothing.
	for _, body := range []string{"", `{}`} {
		do("POST", "/api/cloud/connect", body, dash, 50*time.Millisecond)
		assert.Equal(t, false, status()["max_mode_saved"], "a plain connect chooses nothing: %q", body)
	}

	// The answer is saved before the enrollment starts.
	do("POST", "/api/cloud/connect", `{"mode":"launch","allow_auto_accept":false}`, dash, 50*time.Millisecond)
	st := status()
	assert.Equal(t, true, st["max_mode_saved"])
	assert.Equal(t, false, st["allow_hub_auto_accept"])
	s, err := store.NewSessionStore(r.db).GetSettings(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "launch", s["cloud_max_mode"])

	do("POST", "/api/cloud/connect", `{"mode":"interact"}`, dash, 50*time.Millisecond)
	s, _ = store.NewSessionStore(r.db).GetSettings(context.Background())
	assert.Equal(t, "interact", s["cloud_max_mode"])
	assert.Equal(t, "false", s["cloud_allow_hub_auto_accept"], "an answer without allow_auto_accept leaves the switch as it is")

	// Never through the tunnel: Coral Hub cannot choose its own mode or the auto-accept switch with a connect.
	before, _ := store.NewSessionStore(r.db).GetSettings(context.Background())
	rec = do("POST", "/api/cloud/connect", `{"mode":"launch","allow_auto_accept":true}`, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}, 0)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	after, _ := store.NewSessionStore(r.db).GetSettings(context.Background())
	assert.Equal(t, before["cloud_max_mode"], after["cloud_max_mode"])
	assert.Equal(t, before["cloud_allow_hub_auto_accept"], after["cloud_allow_hub_auto_accept"])
}
