package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
)

// mockGitHubAPI sets up an httptest server that returns the given tag_name.
// Returns the server (caller must defer Close) and restores the original URL on cleanup.
func mockGitHubAPI(t *testing.T, tagName string, statusCode int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(map[string]string{"tag_name": tagName})
	}))
	origURL := githubReleasesAPI
	githubReleasesAPI = server.URL
	t.Cleanup(func() {
		githubReleasesAPI = origURL
		server.Close()
	})
	return server
}

// mockGitHubAPIError sets up an httptest server that immediately closes connections.
func mockGitHubAPIError(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return invalid JSON to trigger decode error
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("invalid json"))
	}))
	origURL := githubReleasesAPI
	githubReleasesAPI = server.URL
	t.Cleanup(func() {
		githubReleasesAPI = origURL
		server.Close()
	})
}

// ── FetchLatestVersion Tests ────────────────────────────────────────

func TestFetchLatestVersion_ReturnsVersion(t *testing.T) {
	mockGitHubAPI(t, "v0.14.0", http.StatusOK)

	version := FetchLatestVersion()
	assert.Equal(t, "0.14.0", version, "should strip 'v' prefix and return version")
}

func TestFetchLatestVersion_NoPrefix(t *testing.T) {
	mockGitHubAPI(t, "0.14.0", http.StatusOK)

	version := FetchLatestVersion()
	assert.Equal(t, "0.14.0", version, "should handle version without 'v' prefix")
}

func TestFetchLatestVersion_GitHubError(t *testing.T) {
	mockGitHubAPIError(t)

	version := FetchLatestVersion()
	assert.Empty(t, version, "should return empty on decode error")
}

func TestFetchLatestVersion_HTTPError(t *testing.T) {
	origURL := githubReleasesAPI
	githubReleasesAPI = "http://127.0.0.1:1" // unreachable port
	t.Cleanup(func() { githubReleasesAPI = origURL })

	version := FetchLatestVersion()
	assert.Empty(t, version, "should return empty on connection error")
}

func TestFetchLatestVersion_Non200(t *testing.T) {
	mockGitHubAPI(t, "", http.StatusNotFound)

	version := FetchLatestVersion()
	assert.Empty(t, version, "should return empty on non-200 status")
}

// ── UpdateCheck Handler Tests ───────────────────────────────────────

func TestUpdateCheck_UpdateAvailable(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	mockGitHubAPI(t, "v0.14.0", http.StatusOK)
	origVersion := config.Version
	config.Version = "0.13.1"
	t.Cleanup(func() { config.Version = origVersion })

	handler := &SystemHandler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/system/update-check", nil)
	handler.UpdateCheck(w, r)

	require.Equal(t, 200, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, true, resp["available"])
	assert.Equal(t, "0.13.1", resp["current"])
	assert.Equal(t, "0.14.0", resp["latest"])
}

func TestUpdateCheck_UpToDate(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	mockGitHubAPI(t, "v0.13.1", http.StatusOK)
	origVersion := config.Version
	config.Version = "0.13.1"
	t.Cleanup(func() { config.Version = origVersion })

	handler := &SystemHandler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/system/update-check", nil)
	handler.UpdateCheck(w, r)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, false, resp["available"])
	assert.Equal(t, "0.13.1", resp["current"])
	assert.Equal(t, "0.13.1", resp["latest"])
}

func TestUpdateCheck_NoVersion(t *testing.T) {
	mockGitHubAPI(t, "v0.14.0", http.StatusOK)
	origVersion := config.Version
	config.Version = ""
	t.Cleanup(func() { config.Version = origVersion })

	handler := &SystemHandler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/system/update-check", nil)
	handler.UpdateCheck(w, r)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, false, resp["available"], "should not show update when current version is empty (dev build)")
}

func TestUpdateCheck_GitHubError(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	mockGitHubAPIError(t)
	origVersion := config.Version
	config.Version = "0.13.1"
	t.Cleanup(func() { config.Version = origVersion })

	handler := &SystemHandler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/system/update-check", nil)
	handler.UpdateCheck(w, r)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, false, resp["available"], "should not crash or show update on GitHub error")
	assert.Equal(t, "0.13.1", resp["current"])
	assert.Equal(t, "", resp["latest"])
}

// skipWhenUpdateCheckIsOff: dev, beta and partner builds skip the update check entirely (UpdateCheck returns
// available=false without asking GitHub), so these tests of the GitHub path only apply to other tiers.
func skipWhenUpdateCheckIsOff(t *testing.T) {
	t.Helper()
	if !config.TierChecksForUpdates {
		t.Skip("dev and beta builds skip the update check; run without -tags dev to exercise it")
	}
}
