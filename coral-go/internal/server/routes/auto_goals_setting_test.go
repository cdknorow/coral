package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/background"
)

// Settings > "Name each agent's goal" (#4204): PUT /api/settings takes auto_goals as a boolean,
// stores it as "true"/"false", and the generator reads "false" as off; a non-boolean is a 400.
func TestAutoGoalsSettingIsABoolean(t *testing.T) {
	server, _ := setupSystemTestServer(t)
	put := func(body map[string]any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("PUT", server.URL+"/api/settings", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	stored := func() map[string]string {
		resp, err := http.Get(server.URL + "/api/settings")
		require.NoError(t, err)
		defer resp.Body.Close()
		var body struct {
			Settings map[string]string `json:"settings"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body.Settings
	}

	assert.Equal(t, http.StatusOK, put(map[string]any{background.GoalSettingKey: false}))
	assert.Equal(t, "false", stored()[background.GoalSettingKey])
	assert.True(t, background.GoalsDisabled(stored()), "off")

	assert.Equal(t, http.StatusOK, put(map[string]any{background.GoalSettingKey: true}))
	assert.Equal(t, "true", stored()[background.GoalSettingKey])
	assert.False(t, background.GoalsDisabled(stored()), "on")

	assert.Equal(t, http.StatusBadRequest, put(map[string]any{background.GoalSettingKey: "sometimes"}))
	assert.Equal(t, "true", stored()[background.GoalSettingKey], "the bad value did not overwrite")
}
