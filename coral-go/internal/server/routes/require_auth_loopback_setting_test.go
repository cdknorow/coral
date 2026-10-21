package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/auth"
)

// PUT /api/settings accepts require_auth_on_loopback as a boolean, stores it normalised to
// "true"/"false", applies it live, and rejects a non-boolean (#3585). The route is Operator-only
// and not reachable through the Coral Hub (operatorOnlyRoutes), so the setting is too.
func TestRequireAuthOnLoopbackSettingIsValidated(t *testing.T) {
	server, _ := setupSystemTestServer(t)
	t.Cleanup(func() { auth.SetRequireAuthOnLoopback(false) })
	put := func(body map[string]any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("PUT", server.URL+"/api/settings", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	stored := func() map[string]any {
		resp, err := http.Get(server.URL + "/api/settings")
		require.NoError(t, err)
		defer resp.Body.Close()
		var body struct {
			Settings map[string]any `json:"settings"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body.Settings
	}

	assert.Equal(t, http.StatusOK, put(map[string]any{auth.RequireAuthOnLoopbackSetting: true}))
	assert.Equal(t, "true", stored()[auth.RequireAuthOnLoopbackSetting])
	assert.True(t, auth.RequireAuthOnLoopback(), "applied live")

	assert.Equal(t, http.StatusOK, put(map[string]any{auth.RequireAuthOnLoopbackSetting: "off"}))
	assert.Equal(t, "false", stored()[auth.RequireAuthOnLoopbackSetting])
	assert.False(t, auth.RequireAuthOnLoopback(), "applied live")

	// A non-boolean is a 400 and nothing is stored from that request.
	assert.Equal(t, http.StatusBadRequest, put(map[string]any{auth.RequireAuthOnLoopbackSetting: "maybe"}))
	assert.Equal(t, "false", stored()[auth.RequireAuthOnLoopbackSetting], "the bad value did not overwrite")
}
