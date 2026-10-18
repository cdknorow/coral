package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settingsClient is PUT and GET /api/settings on a test server in an isolated environment (the #4381 rules).
func settingsClient(t *testing.T) (put func(raw string) int, stored func() map[string]any) {
	t.Helper()
	iso := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, iso)
	}
	t.Setenv("HOME", filepath.Join(iso, "home"))
	t.Setenv("USERPROFILE", filepath.Join(iso, "home"))
	t.Setenv("CORAL_DATA_DIR", filepath.Join(iso, "data"))
	var server *httptest.Server
	server, _ = setupSystemTestServer(t)
	put = func(raw string) int {
		req, _ := http.NewRequest(http.MethodPut, server.URL+"/api/settings", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	stored = func() map[string]any {
		resp, err := http.Get(server.URL + "/api/settings")
		require.NoError(t, err)
		defer resp.Body.Close()
		var body struct {
			Settings map[string]any `json:"settings"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body.Settings
	}
	return put, stored
}

// #4432 (Security's #4431 SHOULD 1): null is an empty setting, never the text "<nil>" (which as a CLI path broke every
// launch), and an array or an object is refused for any key, with nothing of the request saved.
func TestSettingValuesAreScalars(t *testing.T) {
	put, stored := settingsClient(t)
	require.Equal(t, http.StatusOK, put(`{"cli_path_claude": null, "default_working_dir": null}`))
	s := stored()
	for _, k := range []string{"cli_path_claude", "default_working_dir"} {
		assert.NotContains(t, s[k], "nil", k)
		assert.Empty(t, s[k], "%s: null is empty", k)
	}

	require.Equal(t, http.StatusOK, put(`{"default_working_dir": "/tmp/kept"}`))
	for _, raw := range []string{
		`{"default_working_dir": "/tmp/changed", "macros": [{"label": "x"}]}`,
		`{"default_working_dir": "/tmp/changed", "saved_personas": {"a": 1}}`,
		`{"cli_path_codex": ["/bin/sh"]}`,
		`{"default_working_dir": []}`,
	} {
		assert.Equal(t, http.StatusBadRequest, put(raw), raw)
	}
	assert.Equal(t, "/tmp/kept", stored()["default_working_dir"], "a refused request saves nothing")
}

// #4432 (Security's #4431 SHOULD 2): a number is stored with all its digits, never in exponent form, a fraction is
// refused, and the artifact store's cap must be a whole number of bytes.
func TestSettingNumbersAreStoredWhole(t *testing.T) {
	put, stored := settingsClient(t)
	require.Equal(t, http.StatusOK, put(`{"artifact_store_max_bytes": 10737418240, "terminal_scrollback": 1000000}`))
	s := stored()
	assert.Equal(t, "10737418240", s["artifact_store_max_bytes"])
	assert.Equal(t, "1000000", s["terminal_scrollback"])
	require.Equal(t, http.StatusOK, put(`{"artifact_store_max_bytes": "0"}`))
	assert.Equal(t, "0", stored()["artifact_store_max_bytes"])
	for _, raw := range []string{
		`{"terminal_font_size": 13.5}`,
		`{"terminal_scrollback": 1e300}`,
		`{"artifact_store_max_bytes": "lots"}`,
		`{"artifact_store_max_bytes": "-1"}`,
		`{"artifact_store_max_bytes": -5}`,
		`{"artifact_store_max_bytes": true}`,
	} {
		assert.Equal(t, http.StatusBadRequest, put(raw), raw)
	}
	assert.Equal(t, "0", stored()["artifact_store_max_bytes"], "a refused value leaves the cap as it was")
}

// #4441: the cap on received files is a whole number of bytes (0 for none, never empty), and its notice's acknowledgement is the cap it was shown for.
func TestReceivedCapSettings(t *testing.T) {
	put, stored := settingsClient(t)
	require.Equal(t, http.StatusOK, put(`{"received_artifacts_max_bytes": 5368709120}`))
	assert.Equal(t, "5368709120", stored()["received_artifacts_max_bytes"])
	require.Equal(t, http.StatusOK, put(`{"received_artifacts_max_bytes": "0"}`))
	assert.Equal(t, "0", stored()["received_artifacts_max_bytes"])
	require.Equal(t, http.StatusOK, put(`{"received_cap_notice_ack": "5368709120"}`))
	assert.Equal(t, "5368709120", stored()["received_cap_notice_ack"])
	for _, raw := range []string{
		`{"received_artifacts_max_bytes": "lots"}`,
		`{"received_artifacts_max_bytes": ""}`,
		`{"received_artifacts_max_bytes": "-1"}`,
		`{"received_artifacts_max_bytes": 1.5}`,
		`{"received_artifacts_max_bytes": true}`,
		`{"received_cap_notice_ack": "lots"}`,
		`{"received_cap_notice_ack": "-1"}`,
		`{"received_cap_notice_ack": true}`,
	} {
		assert.Equal(t, http.StatusBadRequest, put(raw), raw)
	}
	assert.Equal(t, "0", stored()["received_artifacts_max_bytes"], "a refused value leaves the cap as it was")
}

// The store's cap reader never reads a value it can't parse as "no cap": the exponent form older versions stored is the
// number it stands for, and anything else, or a settings read that fails, takes nothing new.
func TestArtifactStoreCapReaderFailsClosed(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	ctx := t.Context()
	assert.Equal(t, defaultArtifactStoreMaxBytes, h.artifactStoreMaxBytes(), "nothing set: the 20 GiB default (#4602), not no cap")
	assert.EqualValues(t, 20<<30, defaultArtifactStoreMaxBytes)
	for value, want := range map[string]int64{
		"":                defaultArtifactStoreMaxBytes, // blank is unset
		"  ":              defaultArtifactStoreMaxBytes,
		"0":               0, // the Operator's explicit no cap
		"10737418240":     10737418240,
		"1.073741824e+10": 10737418240, // as v1.4.80 and earlier stored 10 GiB
		"lots":            artifactStoreRefuse,
		"-1":              artifactStoreRefuse,
		"1.5":             artifactStoreRefuse,
	} {
		require.NoError(t, ss.SetSetting(ctx, ArtifactStoreMaxBytesSetting, value))
		assert.Equal(t, want, h.artifactStoreMaxBytes(), "%q", value)
	}
}

// Security's ruling on #4432: null is empty for every key, so a key whose validator refuses an empty value refuses null
// too; the boolean keys that read empty as false (auto_goals, require_auth_on_loopback) store false, as an Operator
// sending false would.
func TestNullIsEmptyForEveryValidator(t *testing.T) {
	put, stored := settingsClient(t)
	for _, k := range []string{"agent_priority", "auto_resume_after_api_error", "auto_wake_cap", "auto_wake_after_restart",
		"artifact_retention_days", "artifact_retention_kinds", "no_such_key"} {
		assert.Equal(t, http.StatusBadRequest, put(`{"`+k+`": null}`), k)
		assert.Equal(t, http.StatusBadRequest, put(`{"`+k+`": ""}`), "%s: the same as empty", k)
	}
	for _, k := range []string{"auto_goals", "require_auth_on_loopback"} {
		require.Equal(t, http.StatusOK, put(`{"`+k+`": true}`), k)
		require.Equal(t, http.StatusOK, put(`{"`+k+`": null}`), k)
		assert.Equal(t, "false", stored()[k], "%s: null is false, as empty is", k)
	}
}
