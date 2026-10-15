package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3554: artifact retention's settings are validated, its bookkeeping keys are Coral's own (a
// client can't back-date the start of the wait), and a changed value records when it was turned
// on, so Settings can offer the review at once.
func TestRetentionSettingsThroughPutSettings(t *testing.T) {
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
	for _, bad := range []map[string]any{
		{RetentionDaysSetting: -1}, {RetentionDaysSetting: 3651}, {RetentionDaysSetting: 1.5}, {RetentionDaysSetting: "soon"},
		{RetentionKindsSetting: "everything"},
		{retentionEnabledAtKey: "2000-01-01T00:00:00Z"}, {retentionSeenDaysKey: "7"}, {RetentionAckAtKey: "2099-01-01T00:00:00Z"}, {RetentionLastKey: "{}"},
	} {
		assert.Equal(t, http.StatusBadRequest, put(bad), "%v", bad)
	}
	before := time.Now().Add(-time.Second)
	require.Equal(t, http.StatusOK, put(map[string]any{RetentionDaysSetting: 14, RetentionKindsSetting: "diffs,objects"}))
	s := stored()
	assert.Equal(t, "14", s[RetentionDaysSetting])
	assert.Equal(t, "diffs,objects", s[RetentionKindsSetting])
	at, err := time.Parse(time.RFC3339, s[retentionEnabledAtKey].(string))
	require.NoError(t, err)
	assert.True(t, !at.Before(before.Truncate(time.Second)), "turned on now, not when the job next looks")
	// The same value again doesn't restart the wait.
	require.Equal(t, http.StatusOK, put(map[string]any{RetentionDaysSetting: "14"}))
	assert.Equal(t, s[retentionEnabledAtKey], stored()[retentionEnabledAtKey])
	require.Equal(t, http.StatusOK, put(map[string]any{RetentionDaysSetting: 0}))
	assert.Equal(t, "", stored()[retentionEnabledAtKey], "off clears it")
}
