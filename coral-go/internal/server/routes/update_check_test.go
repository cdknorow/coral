package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updatecheck"
)

// feedSeq numbers the feed URLs the tests hand the update check.
var feedSeq atomic.Int64

// uniqueFeedURL is base with a path no other test has used. The update check caches its answer per feed URL for an hour, and a test
// server's address (a loopback port) is reused once an earlier test's server is closed: a later test whose server got the same port
// was answered from the earlier test's cached feed (TestUpdateCheck_NeverOffersAnOlderRelease failed in full runs, never alone).
func uniqueFeedURL(base string) string { return fmt.Sprintf("%s/feed-%d", base, feedSeq.Add(1)) }

// mockGitHubAPI sets up an httptest server that returns the given tag_name.
// Returns the server (caller must defer Close) and restores the original URL on cleanup.
func mockGitHubAPI(t *testing.T, tagName string, statusCode int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		// the page URL is part of the reply the update check requires (#5557): this repository's page for the tag
		json.NewEncoder(w).Encode(map[string]any{"tag_name": tagName, "draft": false, "prerelease": false, "html_url": "https://github.com/subgentic/coral/releases/tag/" + tagName})
	}))
	origURL := githubReleasesAPI
	githubReleasesAPI = uniqueFeedURL(server.URL)
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
	githubReleasesAPI = uniqueFeedURL(server.URL)
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
	githubReleasesAPI = uniqueFeedURL("http://127.0.0.1:1") // unreachable port
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
	skipWhenUpdateCheckIsOff(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v99.0.0", "html_url": "https://github.com/subgentic/coral/releases/tag/v99.0.0"})
	}))
	origURL, origVersion := githubReleasesAPI, config.Version
	githubReleasesAPI = uniqueFeedURL(server.URL)
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

// The cause of the order-dependent failure, made deterministic: a closed test server's address is bound again by the next test's server,
// and the update check answers from what it cached for that address; a feed URL made with uniqueFeedURL is a different cache key.
func TestUpdateCheckCacheIsNotSharedBetweenTestServersOnOneAddress(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	feed := func(tag string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "html_url": "https://github.com/subgentic/coral/releases/tag/" + tag})
		})
	}
	latest := func(u string) string {
		v, _ := updatecheck.Default.Latest(context.Background(), u)
		return v
	}
	first := httptest.NewServer(feed("v90.0.0"))
	addr := first.Listener.Addr().String()
	bare := first.URL
	require.Equal(t, "90.0.0", latest(bare), "the first server's answer is cached for its address")
	first.Close()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("the address was not free again: %v", err)
	}
	second := httptest.NewUnstartedServer(feed("v91.0.0"))
	second.Listener.Close()
	second.Listener = ln
	second.Start()
	defer second.Close()

	assert.Equal(t, "90.0.0", latest(second.URL), "the bare address is answered from the earlier test's cache: the bug")
	assert.Equal(t, "91.0.0", latest(uniqueFeedURL(second.URL)), "a unique feed URL is asked for itself")
}
