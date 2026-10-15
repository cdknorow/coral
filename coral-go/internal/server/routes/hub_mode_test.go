package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// Settings > Run as a hub (#3460): saved from the dashboard on this computer only, applied
// at the next start; --hub keeps it on; Restart only where the process can.
func TestHubModeSetting(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	ss := store.NewSessionStore(db)
	h := NewCloudHandler(ss, t.TempDir())
	peer := [2]bool{true, true} // a real loopback request from Coral's own user (#3442)
	h.SetPeerCheck(func(*http.Request) (bool, bool) { return peer[0], peer[1] })
	req := func(method, path, body string, local bool) *httptest.ResponseRecorder {
		var r *http.Request
		if local {
			r = httptest.NewRequest(method, "http://127.0.0.1:8420"+path, strings.NewReader(body))
			r.RemoteAddr = "127.0.0.1:5555"
			r.Header.Set("Origin", "http://127.0.0.1:8420")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			// As the identity middleware marks this computer's dashboard (#3634: restart needs a person).
			r = r.WithContext(context.WithValue(r.Context(), callerKindKey{}, callerDashCookie))
		} else {
			r = stamp(httptest.NewRequest(method, path, strings.NewReader(body)))
		}
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if path == "/api/system/restart" {
			h.RestartServer(rec, r)
		} else {
			h.HubMode(rec, r)
		}
		return rec
	}

	rec := req("GET", "/api/system/hub-mode", "", true)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"saved":false,"running":false,"forced":false,"restart_required":false,"can_restart":false}`, rec.Body.String())

	rec = req("PUT", "/api/system/hub-mode", `{"enabled":true}`, true)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"saved":true,"running":false,"forced":false,"restart_required":true,"can_restart":false}`, rec.Body.String())
	s, _ := ss.GetSettings(t.Context())
	assert.Equal(t, "true", s[HubModeSetting])
	require.Equal(t, 400, req("PUT", "/api/system/hub-mode", `{}`, true).Code)

	// Never through Coral Hub, never another local user.
	for _, m := range []string{"GET", "PUT"} {
		assert.Equal(t, 403, req(m, "/api/system/hub-mode", `{"enabled":false}`, false).Code, "tunneled "+m)
	}
	assert.Equal(t, 403, req("POST", "/api/system/restart", `{}`, false).Code)
	peer = [2]bool{false, true}
	assert.Equal(t, 403, req("PUT", "/api/system/hub-mode", `{"enabled":false}`, true).Code)
	assert.Equal(t, 403, req("POST", "/api/system/restart", `{}`, true).Code)
	peer = [2]bool{true, true}
	// The refusals point at this computer's dashboard, never at coral cloud connect (QA, #3460).
	for _, rec := range []*httptest.ResponseRecorder{req("PUT", "/api/system/hub-mode", `{"enabled":false}`, false), req("POST", "/api/system/restart", `{}`, false)} {
		assert.Contains(t, rec.Body.String(), "Coral dashboard on the computer running Coral")
		assert.NotContains(t, rec.Body.String(), "cloud connect")
	}
	notDashboard := httptest.NewRequest("PUT", "http://127.0.0.1:8420/api/system/hub-mode", strings.NewReader(`{"enabled":false}`))
	notDashboard.RemoteAddr = "127.0.0.1:5555"
	notDashboard.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.HubMode(rec, notDashboard)
	assert.Equal(t, 403, rec.Code, "a loopback request that isn't the dashboard")
	assert.Contains(t, rec.Body.String(), `"code":"local_dashboard_required"`)
	assert.NotContains(t, rec.Body.String(), "cloud connect")
	s, _ = ss.GetSettings(t.Context())
	assert.Equal(t, "true", s[HubModeSetting], "refused requests changed nothing")

	// Restart: 409 where the process can't; called where it can.
	require.Equal(t, 409, req("POST", "/api/system/restart", `{}`, true).Code)
	restarts := 0
	h.Restart = func() error { restarts++; return nil }
	rec = req("GET", "/api/system/hub-mode", "", true)
	assert.Contains(t, rec.Body.String(), `"can_restart":true`)
	require.Equal(t, 202, req("POST", "/api/system/restart", `{}`, true).Code)
	assert.Equal(t, 1, restarts)

	// Running as a hub with it saved: nothing to apply. Forced by --hub: on, whatever is saved.
	h.HubModeRunning = true
	assert.Contains(t, req("GET", "/api/system/hub-mode", "", true).Body.String(), `"restart_required":false`)
	h.HubModeForced = true
	rec = req("PUT", "/api/system/hub-mode", `{"enabled":false}`, true)
	assert.JSONEq(t, `{"saved":false,"running":true,"forced":true,"restart_required":false,"can_restart":true}`, rec.Body.String())
}
