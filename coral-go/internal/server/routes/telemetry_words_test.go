package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// telemetry_enabled is on unless it is turned off, and PUT /api/settings accepts a JSON false (stored "False") and the words off, 0 and no: every
// one of them turns it off in what the privacy view reports, and a value that is not one of them does not (#5129; "False" and "off" were read as on).
func TestTelemetrySettingIsOffForEveryWordForOff(t *testing.T) {
	server, _ := setupSystemTestServer(t)
	enabled := func() bool {
		resp, err := http.Get(server.URL + "/api/system/privacy")
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out["telemetry_enabled"] == true
	}
	put := func(body string) {
		req, _ := http.NewRequest("PUT", server.URL+"/api/settings", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
	}
	assert.True(t, enabled(), "on by default")
	for _, off := range []string{`{"telemetry_enabled":false}`, `{"telemetry_enabled":"false"}`, `{"telemetry_enabled":"False"}`, `{"telemetry_enabled":"off"}`, `{"telemetry_enabled":"0"}`, `{"telemetry_enabled":"no"}`} {
		put(`{"telemetry_enabled":true}`)
		require.True(t, enabled(), "back on before "+off)
		put(off)
		assert.False(t, enabled(), off)
	}
	for _, on := range []string{`{"telemetry_enabled":true}`, `{"telemetry_enabled":"true"}`, `{"telemetry_enabled":"yes"}`, `{"telemetry_enabled":"1"}`} {
		put(`{"telemetry_enabled":false}`)
		put(on)
		assert.True(t, enabled(), on)
	}
}
