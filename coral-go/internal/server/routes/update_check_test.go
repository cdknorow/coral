package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

// #3660: a build ahead of the latest release (or a release candidate of the next one) is never
// told to move to the older release.
func TestUpdateCheck_NeverOffersAnOlderRelease(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	mockGitHubAPI(t, "v0.14.0", http.StatusOK)
	origVersion := config.Version
	t.Cleanup(func() { config.Version = origVersion })
	for _, current := range []string{"0.15.0", "0.14.1-rc1", "0.14.0"} {
		config.Version = current
		w := httptest.NewRecorder()
		(&SystemHandler{}).UpdateCheck(w, httptest.NewRequest("GET", "/api/system/update-check", nil))
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, false, resp["available"], "current %s", current)
	}
}

// ── #4133: the route does not make one GitHub request per call ──────────

func TestUpdateCheckTwentyCallsMakeOneRequest(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		json.NewEncoder(w).Encode(map[string]string{"tag_name": "v99.0.0"})
	}))
	origURL, origVersion := githubReleasesAPI, config.Version
	githubReleasesAPI = server.URL
	config.Version = "1.4.70"
	t.Cleanup(func() { githubReleasesAPI, config.Version = origURL, origVersion; server.Close() })

	h := &SystemHandler{}
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		h.UpdateCheck(rec, httptest.NewRequest("GET", "/api/system/update-check", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		assert.Equal(t, true, out["available"])
		assert.Equal(t, "99.0.0", out["latest"])
	}
	assert.EqualValues(t, 1, hits.Load(), "20 calls to the route, one request to GitHub")

	// A manual check right after is the cached answer too; it cannot be used to hammer the feed.
	for i := 0; i < 10; i++ {
		h.UpdateCheck(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/system/update-check?refresh=1", nil))
	}
	assert.EqualValues(t, 1, hits.Load(), "?refresh=1 within a minute of the last answer is not another request")
}
