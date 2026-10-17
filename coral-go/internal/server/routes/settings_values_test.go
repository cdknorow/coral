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

// The store's cap reader never reads a value it can't parse as "no cap": the exponent form older versions stored is the
// number it stands for, and anything else, or a settings read that fails, takes nothing new.
func TestArtifactStoreCapReaderFailsClosed(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	ctx := t.Context()
	for value, want := range map[string]int64{
		"":                0,
		"0":               0,
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
