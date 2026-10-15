package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agentprio"
)

// PUT /api/settings accepts agent_priority "low" or "normal" only (#3470): an unknown
// value is a 400 and nothing in that request is saved; a valid one is stored
// normalised and applied.
func TestAgentPrioritySettingIsValidated(t *testing.T) {
	server, _ := setupSystemTestServer(t)
	t.Cleanup(func() { agentprio.SetSetting("") })
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

	assert.Equal(t, http.StatusOK, put(map[string]any{"agent_priority": " Normal "}))
	assert.Equal(t, "normal", stored()["agent_priority"], "stored normalised")
	assert.Equal(t, agentprio.Normal, agentprio.Level())

	for _, bad := range []any{"turbo", "", 5, true, nil} {
		assert.Equal(t, http.StatusBadRequest, put(map[string]any{"agent_priority": bad, "theme": "x-should-not-save"}), "%v", bad)
	}
	assert.Equal(t, "normal", stored()["agent_priority"], "a rejected value changes nothing")
	assert.NotEqual(t, "x-should-not-save", stored()["theme"], "nothing in a rejected request is saved")
	assert.Equal(t, agentprio.Normal, agentprio.Level())

	assert.Equal(t, http.StatusOK, put(map[string]any{"agent_priority": "low"}))
	assert.Equal(t, agentprio.Low, agentprio.Level())
}
