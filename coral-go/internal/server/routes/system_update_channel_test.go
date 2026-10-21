package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

// The release channels on the server side (#5557): the update_channel setting chooses between two feeds, GET /api/system/update-check and
// GET /api/system/update tell the dashboard which channel it is on and what waits for approval.

const pageBase = "https://github.com/subgentic/coral/releases/tag/"

type feedRel map[string]any

func channelRelease(tag string, approved bool, body string) feedRel {
	return feedRel{"tag_name": tag, "draft": false, "prerelease": !approved, "html_url": pageBase + tag, "body": body, "published_at": "2026-10-20T10:00:00Z"}
}

// channelFeeds starts the two feeds and points the server at them. approvedHits and agenticHits count the requests each one got.
func channelFeeds(t *testing.T, latest feedRel, list ...feedRel) (approvedHits, agenticHits *atomic.Int32) {
	t.Helper()
	approvedHits, agenticHits = new(atomic.Int32), new(atomic.Int32)
	serve := func(hits *atomic.Int32, v any) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}))
		t.Cleanup(s.Close)
		return s
	}
	a, g := serve(approvedHits, latest), serve(agenticHits, list)
	origA, origG := githubReleasesAPI, githubReleasesListAPI
	githubReleasesAPI, githubReleasesListAPI = uniqueFeedURL(a.URL), uniqueFeedURL(g.URL)
	t.Cleanup(func() { githubReleasesAPI, githubReleasesListAPI = origA, origG })
	return approvedHits, agenticHits
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	orig := config.Version
	config.Version = v
	t.Cleanup(func() { config.Version = orig })
}

func channelGetJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func putSetting(t *testing.T, base, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, base+"/api/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	return resp.StatusCode
}

func TestUpdateCheckIsOnTheApprovedChannelByDefaultAndNeverAsksTheAgenticFeed(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	withVersion(t, "1.4.141")
	approvedHits, agenticHits := channelFeeds(t, channelRelease("v1.4.142", true, "n"), channelRelease("v1.4.144", false, "n"), channelRelease("v1.4.142", true, "n"))
	server, _ := setupSystemTestServer(t)
	got := channelGetJSON(t, server.URL+"/api/system/update-check")
	assert.Equal(t, "approved", got["channel"])
	assert.Equal(t, true, got["available"])
	assert.Equal(t, "1.4.142", got["latest"])
	assert.Equal(t, true, got["approved"])
	assert.Equal(t, "https://github.com/subgentic/coral/releases", got["release_url"])
	assert.EqualValues(t, 1, approvedHits.Load())
	assert.EqualValues(t, 0, agenticHits.Load(), "an Approved user never touches the list feed")
}

func TestUpdateCheckOnTheAgenticChannelOffersTheHighestPlainReleaseAndSaysItIsNotApproved(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	withVersion(t, "1.4.141")
	approvedHits, agenticHits := channelFeeds(t, channelRelease("v1.4.142", true, "n"),
		channelRelease("v1.4.143-rc1", false, "n"), channelRelease("v1.4.144", false, "n"), channelRelease("v1.4.142", true, "n"))
	server, _ := setupSystemTestServer(t)
	require.Equal(t, 200, putSetting(t, server.URL, `{"update_channel":"agentic"}`))
	got := channelGetJSON(t, server.URL+"/api/system/update-check")
	assert.Equal(t, "agentic", got["channel"])
	assert.Equal(t, true, got["available"])
	assert.Equal(t, "1.4.144", got["latest"], "the rc is not offered, the highest plain version is")
	assert.Equal(t, false, got["approved"])
	assert.Equal(t, "https://github.com/subgentic/coral/releases/tag/v1.4.144", got["release_url"], "the release's own page, built from the version")
	assert.EqualValues(t, 1, agenticHits.Load())
	assert.EqualValues(t, 0, approvedHits.Load())

	// back to approved: the other feed answers, no flush needed
	require.Equal(t, 200, putSetting(t, server.URL, `{"update_channel":"approved"}`))
	got = channelGetJSON(t, server.URL+"/api/system/update-check")
	assert.Equal(t, "approved", got["channel"])
	assert.Equal(t, "1.4.142", got["latest"])
	assert.Equal(t, true, got["approved"])
}

// A build newer than the Approved release is not sent back to it when the person goes from Agentic to Approved.
func TestSwitchingToApprovedNeverOffersADowngrade(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	withVersion(t, "1.4.144")
	channelFeeds(t, channelRelease("v1.4.142", true, "n"), channelRelease("v1.4.144", true, "n"))
	server, _ := setupSystemTestServer(t)
	got := channelGetJSON(t, server.URL+"/api/system/update-check")
	assert.Equal(t, false, got["available"])
	assert.Equal(t, "1.4.142", got["latest"])
}

func TestAChannelNobodyCanExplainIsApprovedAndTheSettingRefusesIt(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	withVersion(t, "1.4.141")
	_, agenticHits := channelFeeds(t, channelRelease("v1.4.142", true, "n"), channelRelease("v1.4.150", false, "n"))
	server, _ := setupSystemTestServer(t)
	for _, bad := range []string{`"nightly"`, `""`, `"approved,agentic"`, `true`, `1`, `null`, `["agentic"]`, `"agentic\u0000"`} {
		code := putSetting(t, server.URL, `{"update_channel":`+bad+`}`)
		if code == 200 { // an empty or null value is stored as unset: it still reads as approved
			assert.Equal(t, "approved", channelGetJSON(t, server.URL+"/api/system/update-check")["channel"], bad)
		}
	}
	assert.EqualValues(t, 0, agenticHits.Load(), "no stored value but the word agentic reaches the list feed")
}

func TestOnADevOrPartnerBuildNothingIsAskedOnEitherChannel(t *testing.T) {
	orig := config.TierChecksForUpdates
	config.TierChecksForUpdates = false
	t.Cleanup(func() { config.TierChecksForUpdates = orig })
	approvedHits, agenticHits := channelFeeds(t, channelRelease("v1.4.142", true, "n"), channelRelease("v1.4.150", false, "n"))
	server, _ := setupSystemTestServer(t)
	require.Equal(t, 200, putSetting(t, server.URL, `{"update_channel":"agentic"}`))
	got := channelGetJSON(t, server.URL+"/api/system/update-check")
	assert.Equal(t, false, got["available"])
	assert.Equal(t, "agentic", got["channel"])
	assert.EqualValues(t, 0, approvedHits.Load()+agenticHits.Load())
}

// ---- GET /api/system/update: channel, approved, past_approved, waiting ----

func statusOn(t *testing.T, e *relaunchEnv, hdr map[string]string) map[string]any {
	t.Helper()
	code, out := e.status(hdr)
	require.Equal(t, 200, code)
	return out
}

// settle polls the status until the background fetch has filled the cache and the fields appear (it never waits for GitHub itself).
func settle(t *testing.T, e *relaunchEnv, hdr map[string]string, field string) map[string]any {
	t.Helper()
	var out map[string]any
	require.Eventually(t, func() bool { out = statusOn(t, e, hdr); _, ok := out[field]; return ok }, 5*time.Second, 20*time.Millisecond, "the %s field never appeared", field)
	return out
}

func TestTheStatusAlwaysNamesTheChannelAndNeverWaitsForGitHub(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	withVersion(t, "1.4.141")
	slow := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-slow }))
	defer func() { close(slow); s.Close() }()
	orig := githubReleasesAPI
	githubReleasesAPI = uniqueFeedURL(s.URL)
	t.Cleanup(func() { githubReleasesAPI = orig })
	e := newRelaunchEnv(t)
	e.h.Feeds = releaseFeeds
	start := time.Now()
	out := statusOn(t, e, nil)
	assert.Less(t, time.Since(start), 2*time.Second, "the status answered while GitHub was still thinking")
	assert.Equal(t, "approved", out["channel"])
	assert.NotContains(t, out, "approved", "nothing is known yet")
	assert.NotContains(t, out, "waiting")
}

func TestTheStatusShowsTheApprovedReleaseAndTheAgeOfItsApproval(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	withVersion(t, "1.4.141")
	channelFeeds(t, channelRelease("v1.4.142", true, "notes\nApproved: 2026-10-20T11:00:00Z"))
	e := newRelaunchEnv(t)
	e.h.Feeds = releaseFeeds
	out := settle(t, e, nil, "approved")
	assert.Equal(t, "approved", out["channel"])
	assert.Equal(t, map[string]any{"version": "1.4.142", "approved_at": "2026-10-20T11:00:00Z"}, out["approved"])
	assert.NotContains(t, out, "past_approved")
	assert.NotContains(t, out, "waiting", "Approved users get no waiting list")
}

func TestTheStatusSaysWhenTheRunningBuildIsPastTheApprovedRelease(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	withVersion(t, "1.4.144")
	channelFeeds(t, channelRelease("v1.4.142", true, "no marker here"))
	e := newRelaunchEnv(t)
	e.h.Feeds = releaseFeeds
	out := settle(t, e, nil, "approved")
	assert.Equal(t, map[string]any{"version": "1.4.142", "approved_at": ""}, out["approved"], "no marker: no age")
	assert.Equal(t, true, out["past_approved"])
}

func TestTheStatusListsWhatWaitsForApprovalOnAgenticForTheOperatorNotTheHub(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	withVersion(t, "1.4.141")
	notes := "first\n" + strings.Repeat("x", 3000)
	channelFeeds(t, channelRelease("v1.4.142", true, "n"),
		channelRelease("v1.4.144", false, notes), channelRelease("v1.4.143", false, "second"), channelRelease("v1.4.142", true, "n"), channelRelease("v1.4.140", false, "superseded"))
	e := newRelaunchEnv(t)
	e.h.Feeds = releaseFeeds
	e.settings["update_channel"] = "agentic"
	out := settle(t, e, nil, "waiting")
	assert.Equal(t, "agentic", out["channel"])
	w := out["waiting"].([]any)
	require.Len(t, w, 2, "newer than the newest approved only; 1.4.140 is superseded")
	first := w[0].(map[string]any)
	assert.Equal(t, "1.4.144", first["version"])
	assert.Equal(t, "2026-10-20T10:00:00Z", first["published_at"])
	assert.LessOrEqual(t, len([]rune(first["notes"].(string))), 2000, "notes are capped")
	assert.Equal(t, "1.4.143", w[1].(map[string]any)["version"])
	assert.Equal(t, "1.4.142", out["approved"].(map[string]any)["version"])

	// through the Hub: the channel and the approved release, no waiting list
	hub := statusOn(t, e, hubHdr(ownerA))
	assert.Equal(t, "agentic", hub["channel"])
	assert.NotContains(t, hub, "waiting")
	assert.Contains(t, hub, "approved")
}

func TestTheStatusKeepsTheChannelWhenTheFeedFails(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer s.Close()
	origA, origG := githubReleasesAPI, githubReleasesListAPI
	githubReleasesAPI, githubReleasesListAPI = uniqueFeedURL(s.URL), uniqueFeedURL(s.URL)
	t.Cleanup(func() { githubReleasesAPI, githubReleasesListAPI = origA, origG })
	e := newRelaunchEnv(t)
	e.h.Feeds = releaseFeeds
	e.settings["update_channel"] = "agentic"
	for i := 0; i < 3; i++ {
		out := statusOn(t, e, nil)
		assert.Equal(t, "agentic", out["channel"])
		assert.NotContains(t, out, "waiting")
		assert.NotContains(t, out, "approved")
		time.Sleep(30 * time.Millisecond)
	}
}

func TestEveryReplyFieldIsAdditiveAndTheNotesAreNeverMarkup(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	withVersion(t, "1.4.141")
	evil := "<img src=x onerror=alert(1)>\u202e [click](javascript:alert(1))\x1b[2J"
	channelFeeds(t, channelRelease("v1.4.142", true, "n"), channelRelease("v1.4.150", false, evil), channelRelease("v1.4.142", true, "n"))
	e := newRelaunchEnv(t)
	e.h.Feeds = releaseFeeds
	e.settings["update_channel"] = "agentic"
	out := settle(t, e, nil, "waiting")
	n := out["waiting"].([]any)[0].(map[string]any)["notes"].(string)
	assert.NotContains(t, n, "\u202e")
	assert.NotContains(t, n, "\x1b")
	assert.Equal(t, fmt.Sprintf("%s [click](javascript:alert(1))[2J", "<img src=x onerror=alert(1)>"), n, "the text is kept as text; the dashboard shows it with textContent")
	for _, k := range []string{"enabled", "installed", "state", "relaunch"} {
		assert.Contains(t, out, k, "the existing fields are still there")
	}
}

// A handler without Feeds (every other test of the status) names the channel and asks nothing: it cannot reach GitHub.
func TestAHandlerWithoutFeedsNamesTheChannelAndAsksNothing(t *testing.T) {
	skipWhenUpdateCheckIsOff(t)
	approvedHits, agenticHits := channelFeeds(t, channelRelease("v1.4.142", true, "n"), channelRelease("v1.4.143", false, "n"))
	e := newRelaunchEnv(t)
	e.settings["update_channel"] = "agentic"
	out := statusOn(t, e, nil)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, "agentic", out["channel"])
	assert.NotContains(t, out, "approved")
	assert.EqualValues(t, 0, approvedHits.Load()+agenticHits.Load())
}
