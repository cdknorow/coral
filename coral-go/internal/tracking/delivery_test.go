package tracking

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawCollector is a fake PostHog that records every request body verbatim and
// answers from a script of status codes (the last one repeats).
type rawCollector struct {
	mu       sync.Mutex
	bodies   [][]byte
	statuses []int
	base     int // requests already seen when the current script was set
}

func (c *rawCollector) set(statuses ...int) {
	c.mu.Lock()
	c.statuses = statuses
	c.base = len(c.bodies)
	c.mu.Unlock()
}

func (c *rawCollector) attempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func (c *rawCollector) payloads(t *testing.T) []map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, b := range c.bodies {
		var m map[string]any
		require.NoError(t, json.Unmarshal(b, &m))
		out = append(out, m)
	}
	return out
}

// newRawCollector isolates tracking state (via newTestTracking) and points the
// capture URL at a recording server. No real analytics call can happen.
func newRawCollector(t *testing.T) (*rawCollector, string) {
	t.Helper()
	_, dir := newTestTracking(t)
	browserMu.Lock()
	seenPages, failuresPerCode = map[string]bool{}, map[string]int{}
	browserMu.Unlock()
	milestoneMu.Lock()
	milestoneInflight = map[string]bool{}
	milestoneMu.Unlock()
	c := &rawCollector{statuses: []int{200}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		i := len(c.bodies) - c.base
		c.bodies = append(c.bodies, body)
		status := c.statuses[len(c.statuses)-1]
		if i >= 0 && i < len(c.statuses) {
			status = c.statuses[i]
		}
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	prev := posthogURL
	posthogURL = srv.URL
	prevDelays := retryDelays
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() {
		waitForAsync()
		srv.Close()
		posthogURL, retryDelays = prev, prevDelays
		SetTelemetryEnabled(true)
	})
	SetTelemetryEnabled(true)
	return c, dir
}

func TestRetriesSendTheIdenticalPayloadAndAreBounded(t *testing.T) {
	c, _ := newRawCollector(t)
	c.set(503, 429, 200)
	TrackEvent(EventSessionLaunched, nil)
	waitForAsync()
	require.Equal(t, 3, c.attempts())
	bodies := c.bodies
	assert.True(t, bytes.Equal(bodies[0], bodies[1]) && bytes.Equal(bodies[1], bodies[2]),
		"every retry must carry the same uuid, event, timestamp, distinct_id and properties")
	p := c.payloads(t)[0]
	for _, key := range []string{"uuid", "event", "timestamp", "distinct_id"} {
		assert.NotEmpty(t, p[key], key)
	}

	// Always failing: bounded at 1 + len(retryDelays) attempts.
	c2, _ := newRawCollector(t)
	c2.set(500)
	TrackEvent(EventSessionLaunched, nil)
	waitForAsync()
	assert.Equal(t, 1+len(retryDelays), c2.attempts())

	// A non-retryable 4xx is not retried.
	c3, _ := newRawCollector(t)
	c3.set(400)
	TrackEvent(EventSessionLaunched, nil)
	waitForAsync()
	assert.Equal(t, 1, c3.attempts())
}

func TestOptOutDuringBackoffStopsRetriesAndOldWorkNeverResumes(t *testing.T) {
	for name, toggle := range map[string]func(){
		"opt out": func() { SetTelemetryEnabled(false) },
		"opt out then back in": func() {
			SetTelemetryEnabled(false)
			SetTelemetryEnabled(true)
		},
	} {
		c, _ := newRawCollector(t)
		c.set(503)
		retryDelays = []time.Duration{300 * time.Millisecond, 300 * time.Millisecond}
		TrackEvent(EventSessionLaunched, nil)
		deadline := time.Now().Add(2 * time.Second)
		for c.attempts() < 1 && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		require.Equal(t, 1, c.attempts(), name)
		toggle() // while the retry sleeps
		waitForAsync()
		assert.Equal(t, 1, c.attempts(), "%s: the queued event must be dropped, not resumed", name)

		// Fresh work after re-enabling is sent normally.
		SetTelemetryEnabled(true)
		c.set(200)
		TrackEvent(EventSessionLaunched, nil)
		waitForAsync()
		assert.Equal(t, 2, c.attempts(), name)
	}
}

func TestOptedOutAndKeylessNeverEmitOrConsumeOneTimeEvents(t *testing.T) {
	c, dir := newRawCollector(t)

	// Opted out: no request, milestone reached (product state), nothing marked sent.
	SetTelemetryEnabled(false)
	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()
	require.Equal(t, 0, c.attempts())
	s := loadMilestonesAt(dir + "/" + milestonesFileName)
	assert.Contains(t, s.Reached, EventFirstAgentLaunched)
	assert.NotContains(t, s.Fired, EventFirstAgentLaunched)
	assert.True(t, ValueDeliveredIn(dir), "the supporter gate keeps working while opted out")

	// Re-enabled: sent exactly once, then never again.
	SetTelemetryEnabled(true)
	TrackOnce(EventFirstAgentLaunched, nil)
	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()
	assert.Equal(t, 1, c.attempts())
	assert.Contains(t, loadMilestonesAt(dir+"/"+milestonesFileName).Fired, EventFirstAgentLaunched)

	// Keyless build: no request and not consumed; a keyed run later sends once.
	config.PostHogKey = ""
	TrackOnce(EventFirstTeamLaunched, map[string]string{"agent_count": "2"})
	waitForAsync()
	assert.Equal(t, 1, c.attempts())
	s = loadMilestonesAt(dir + "/" + milestonesFileName)
	assert.Contains(t, s.Reached, EventFirstTeamLaunched)
	assert.NotContains(t, s.Fired, EventFirstTeamLaunched)
	config.PostHogKey = "phc_test_key"
	TrackOnce(EventFirstTeamLaunched, map[string]string{"agent_count": "2"})
	waitForAsync()
	assert.Equal(t, 2, c.attempts())
}

func TestMilestoneIsMarkedSentOnlyAfterAcceptanceAndRetriesKeepIdentity(t *testing.T) {
	c, dir := newRawCollector(t)
	c.set(500)
	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()
	require.Equal(t, 1+len(retryDelays), c.attempts())
	s := loadMilestonesAt(dir + "/" + milestonesFileName)
	assert.NotContains(t, s.Fired, EventFirstAgentLaunched, "a failed delivery must not consume the milestone")
	require.NotEmpty(t, s.Pending[EventFirstAgentLaunched].Timestamp, "the first-attempt time is kept for later retries")

	// A later trigger (as after a restart) retries with the same identity.
	c.set(200)
	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()
	payloads := c.payloads(t)
	first, last := payloads[0], payloads[len(payloads)-1]
	for _, key := range []string{"uuid", "event", "timestamp", "distinct_id"} {
		assert.Equal(t, first[key], last[key], key)
	}
	s = loadMilestonesAt(dir + "/" + milestonesFileName)
	assert.Contains(t, s.Fired, EventFirstAgentLaunched)
	assert.NotContains(t, s.Pending, EventFirstAgentLaunched)
	before := c.attempts()
	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()
	assert.Equal(t, before, c.attempts(), "sent once, never again")
}

func TestEnvironmentDisableOverridesSavedOptIn(t *testing.T) {
	c, _ := newRawCollector(t)
	SetTelemetryEnabled(true) // saved opt-in
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(envTelemetryDisabled, v)
		assert.False(t, telemetryEnabled(), v)
		TrackEvent(EventSessionLaunched, nil)
		TrackOnce(EventFirstAgentLaunched, nil)
		TrackInstallAsync()
	}
	waitForAsync()
	assert.Equal(t, 0, c.attempts(), "CORAL_TELEMETRY_DISABLED must stop every producer")
	for _, v := range []string{"", "0", "false", "no"} {
		t.Setenv(envTelemetryDisabled, v)
		assert.True(t, telemetryEnabled(), "%q is not a disable", v)
	}
}

func TestOnlyAllowlistedTypedPropertiesAreSent(t *testing.T) {
	c, _ := newRawCollector(t)
	TrackEvent("not_an_event", map[string]string{"x": "y"})
	TrackEvent(EventLaunchResult, map[string]string{
		"kind": "team", "provider": "claude", "backend": "tmux", "outcome": "partial",
		"failure_category": "spawn_failed", "duration_ms": "1234", "requested_agents": "3", "started_agents": "2", "failed_agents": "1",
		"resume": "false", "attempt_id": "abc-123",
		// Everything below must be dropped.
		"error": "open /Users/alice/secret: permission denied", "working_dir": "/Users/alice/repo",
		"prompt": "write my passwords", "session_id": "sess-1",
	})
	TrackEvent(EventLaunchResult, map[string]string{"kind": "agent", "provider": "weird provider", "duration_ms": "99999999", "requested_agents": "-1", "outcome": "boom", "attempt_id": "has space/slash"})
	waitForAsync()

	payloads := c.payloads(t)
	require.Len(t, payloads, 2, "the unknown event is not sent")
	// The two events are sent concurrently; tell them apart by content.
	var good, bad map[string]any
	var goodBody string
	for i, p := range payloads {
		pr := p["properties"].(map[string]any)
		if pr["kind"] == "team" {
			good, goodBody = pr, string(c.bodies[i])
		} else {
			bad = pr
		}
	}
	require.NotNil(t, good)
	require.NotNil(t, bad)
	props := good
	assert.Equal(t, float64(1234), props["duration_ms"], "numbers stay typed")
	assert.Equal(t, false, props["resume"], "booleans stay typed")
	assert.Equal(t, "partial", props["outcome"])
	assert.Equal(t, float64(SchemaVersion), props["schema_version"])
	assert.Equal(t, RunID(), props["run_id"])
	for _, banned := range []string{"error", "working_dir", "prompt", "session_id"} {
		assert.NotContains(t, props, banned)
	}
	assert.NotContains(t, goodBody, "/Users/alice")
	assert.NotContains(t, goodBody, "passwords")

	for _, dropped := range []string{"provider", "duration_ms", "requested_agents", "outcome", "attempt_id"} {
		assert.NotContains(t, bad, dropped, "invalid value for %s must be dropped", dropped)
	}
	assert.Equal(t, "agent", bad["kind"])
}

func TestLaunchAttemptCorrelatesRequestedAndResult(t *testing.T) {
	c, _ := newRawCollector(t)
	a := StartLaunch(KindTeam, ProviderFor("claude"), "tmux", 3, false)
	time.Sleep(5 * time.Millisecond)
	a.Finish(OutcomePartial, FailSpawnFailed, 2, 1)
	var nilAttempt *Attempt
	nilAttempt.Finish(OutcomeSuccess, "", 1, 0) // nil-safe
	waitForAsync()

	var requested, result map[string]any
	for _, p := range c.payloads(t) {
		switch p["event"] {
		case EventLaunchRequested:
			requested = p["properties"].(map[string]any)
		case EventLaunchResult:
			result = p["properties"].(map[string]any)
		}
	}
	require.NotNil(t, requested)
	require.NotNil(t, result)
	assert.NotEmpty(t, requested["attempt_id"])
	assert.Equal(t, requested["attempt_id"], result["attempt_id"])
	assert.Equal(t, "partial", result["outcome"])
	assert.Equal(t, "spawn_failed", result["failure_category"])
	assert.Equal(t, float64(2), result["started_agents"])
	assert.Equal(t, float64(1), result["failed_agents"])
	assert.GreaterOrEqual(t, result["duration_ms"].(float64), float64(5))
	assert.Equal(t, "other", ProviderFor("antigravity"))
	assert.Equal(t, "gemini", ProviderFor(" Gemini "))
	// Two attempts never share an ID.
	assert.NotEqual(t, StartLaunch(KindAgent, "claude", "tmux", 1, false).id, StartLaunch(KindAgent, "claude", "tmux", 1, false).id)
	waitForAsync()
}

func TestBrowserEventsAreRestrictedAndDeduplicatedByTheServer(t *testing.T) {
	c, dir := newRawCollector(t)
	count := func(event string) int {
		n := 0
		for _, p := range c.payloads(t) {
			if p["event"] == event {
				n++
			}
		}
		return n
	}
	// Server-only lifecycle events cannot be sent from the browser.
	for _, e := range []string{EventLaunchResult, EventFirstAgentLaunched, EventTaskCompleted, EventFirstPromptSubmitted, "nonsense"} {
		TrackBrowserEvent(e, map[string]string{"outcome": "success"})
	}
	waitForAsync()
	assert.Equal(t, 0, c.attempts())

	// dashboard_ready: once per page_id; a new page counts; no page_id drops.
	TrackBrowserEvent(EventDashboardReady, map[string]string{"page_id": "page-aaaa"})
	TrackBrowserEvent(EventDashboardReady, map[string]string{"page_id": "page-aaaa"})
	TrackBrowserEvent(EventDashboardReady, map[string]string{"page_id": "page-bbbb"})
	TrackBrowserEvent(EventDashboardReady, nil)
	TrackBrowserEvent(EventDashboardReady, map[string]string{"page_id": "has space/../x"})
	waitForAsync()
	assert.Equal(t, 2, count(EventDashboardReady))
	for _, pl := range c.payloads(t) {
		if pl["event"] == EventDashboardReady {
			assert.NotEmpty(t, pl["properties"].(map[string]any)["page_id"], "dashboard_ready carries its page_id")
		}
	}

	// dashboard_failed: fixed codes only, capped per code.
	for i := 0; i < maxFailuresPerCode+3; i++ {
		TrackBrowserEvent(EventDashboardFailed, map[string]string{"code": "sessions_fetch_http"})
	}
	TrackBrowserEvent(EventDashboardFailed, map[string]string{"code": "status_fetch_invalid"})
	TrackBrowserEvent(EventDashboardFailed, map[string]string{"code": "free text /Users/alice"})
	waitForAsync()
	assert.Equal(t, maxFailuresPerCode+1, count(EventDashboardFailed))

	// dashboard_active_day: once per UTC day, retried until accepted.
	c.set(500, 500, 500, 200)
	TrackBrowserEvent(EventDashboardActiveDay, nil)
	waitForAsync()
	before := count(EventDashboardActiveDay)
	require.Equal(t, 3, before, "the failed day is retried within the bound")
	TrackBrowserEvent(EventDashboardActiveDay, nil) // later trigger, still unsent
	waitForAsync()
	assert.Equal(t, 4, count(EventDashboardActiveDay))
	TrackBrowserEvent(EventDashboardActiveDay, nil)
	TrackBrowserEvent(EventDashboardActiveDay, nil)
	waitForAsync()
	assert.Equal(t, 4, count(EventDashboardActiveDay), "accepted: no more today")
	assert.True(t, strings.Contains(string(mustRead(t, dir+"/"+milestonesFileName)), EventDashboardActiveDay))

	// prompt_submit_requested: once per install; a bad source is dropped.
	c.set(200)
	TrackBrowserEvent(EventPromptSubmitRequested, map[string]string{"source": "dashboard_composer"})
	TrackBrowserEvent(EventPromptSubmitRequested, map[string]string{"source": "dashboard_composer"})
	waitForAsync()
	assert.Equal(t, 1, count(EventPromptSubmitRequested))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

// The one-time event is frozen at its original occurrence: a later trigger with
// different properties must not rewrite what an earlier, undelivered attempt
// would have sent.
func TestPendingMilestoneKeepsItsOriginalOutcomeAcrossLaterTriggers(t *testing.T) {
	c, dir := newRawCollector(t)
	c.set(500)
	TrackOnce(EventFirstTaskCompleted, map[string]string{"outcome": "failed", "junk": "x"})
	waitForAsync()
	require.Equal(t, 1+len(retryDelays), c.attempts())
	s := loadMilestonesAt(dir + "/" + milestonesFileName)
	frozen := s.Pending[EventFirstTaskCompleted]
	require.NotEmpty(t, frozen.Timestamp)
	assert.Equal(t, map[string]string{"outcome": "failed"}, frozen.Props, "only allowlisted props are frozen")

	// A later SUCCESSFUL completion is the trigger that finally delivers.
	c.set(200)
	TrackOnce(EventFirstTaskCompleted, map[string]string{"outcome": "success"})
	waitForAsync()
	payloads := c.payloads(t)
	first, last := payloads[0], payloads[len(payloads)-1]
	for _, key := range []string{"uuid", "event", "timestamp", "distinct_id"} {
		assert.Equal(t, first[key], last[key], key)
	}
	assert.Equal(t, "failed", last["properties"].(map[string]any)["outcome"], "the original occurrence's outcome is what is reported")
	s = loadMilestonesAt(dir + "/" + milestonesFileName)
	assert.Contains(t, s.Fired, EventFirstTaskCompleted)
	assert.NotContains(t, s.Pending, EventFirstTaskCompleted)
}

// Opting out clears undelivered one-time events (no replay of an old event
// after a later opt-in) but keeps product state; a fresh occurrence starts a new snapshot.
func TestOptOutClearsPendingSnapshotsButKeepsReached(t *testing.T) {
	c, dir := newRawCollector(t)
	c.set(500)
	TrackOnce(EventFirstTaskCompleted, map[string]string{"outcome": "failed"})
	waitForAsync()
	oldSnapshot := loadMilestonesAt(dir + "/" + milestonesFileName).Pending[EventFirstTaskCompleted]
	require.NotEmpty(t, oldSnapshot.Timestamp)
	attemptsBefore := c.attempts()

	SetTelemetryEnabled(false)
	s := loadMilestonesAt(dir + "/" + milestonesFileName)
	assert.Empty(t, s.Pending, "opt-out clears the pending snapshot")
	assert.Contains(t, s.Reached, EventFirstTaskCompleted, "Reached is product state and stays")
	assert.NotContains(t, s.Fired, EventFirstTaskCompleted)

	// Re-enabled: nothing old is replayed; the next occurrence is a new snapshot.
	SetTelemetryEnabled(true)
	c.set(200)
	waitForAsync()
	assert.Equal(t, attemptsBefore, c.attempts(), "no replay on opt-in")
	TrackOnce(EventFirstTaskCompleted, map[string]string{"outcome": "success"})
	waitForAsync()
	last := c.payloads(t)[c.attempts()-1]
	assert.Equal(t, "success", last["properties"].(map[string]any)["outcome"])
	assert.NotEqual(t, oldSnapshot.Timestamp, last["timestamp"])
}

func TestEnvironmentDisableAlsoClearsPendingSnapshots(t *testing.T) {
	c, dir := newRawCollector(t)
	c.set(500)
	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()
	require.NotEmpty(t, loadMilestonesAt(dir+"/"+milestonesFileName).Pending)
	t.Setenv(envTelemetryDisabled, "1")
	TrackOnce(EventFirstAgentLaunched, nil) // any later trigger observes the override
	waitForAsync()
	s := loadMilestonesAt(dir + "/" + milestonesFileName)
	assert.Empty(t, s.Pending)
	assert.Contains(t, s.Reached, EventFirstAgentLaunched)
}

// Browser dedupe state is consumed only when an event could be sent.
func TestBrowserDedupeIsNotConsumedWhileDisabledOrKeyless(t *testing.T) {
	c, _ := newRawCollector(t)
	count := func(event string) int {
		n := 0
		for _, p := range c.payloads(t) {
			if p["event"] == event {
				n++
			}
		}
		return n
	}
	page := map[string]string{"page_id": "page-keep-1"}

	SetTelemetryEnabled(false)
	TrackBrowserEvent(EventDashboardReady, page)
	for i := 0; i < maxFailuresPerCode+2; i++ {
		TrackBrowserEvent(EventDashboardFailed, map[string]string{"code": "init_failed"})
	}
	SetTelemetryEnabled(true)
	config.PostHogKey = ""
	TrackBrowserEvent(EventDashboardReady, page)
	TrackBrowserEvent(EventDashboardFailed, map[string]string{"code": "init_failed"})
	config.PostHogKey = "phc_test_key"
	waitForAsync()
	require.Equal(t, 0, c.attempts(), "nothing is sent while disabled or keyless")

	// Now eligible: the same page and code still count, within their caps.
	TrackBrowserEvent(EventDashboardReady, page)
	TrackBrowserEvent(EventDashboardReady, page) // genuine duplicate
	for i := 0; i < maxFailuresPerCode+2; i++ {
		TrackBrowserEvent(EventDashboardFailed, map[string]string{"code": "init_failed"})
	}
	waitForAsync()
	assert.Equal(t, 1, count(EventDashboardReady))
	assert.Equal(t, maxFailuresPerCode, count(EventDashboardFailed))
}

func TestProviderLabelsUseTheEffectiveFallbackAndTrueMixing(t *testing.T) {
	assert.Equal(t, "claude", ProviderFor(""), "blank falls back to Claude like launchSession")
	assert.Equal(t, "other", ProviderFor("antigravity"))
	for types, want := range map[string]string{
		"":                  "claude",
		"codex":             "codex",
		"codex,codex":       "codex",
		",codex":            "mixed", // a blank member is Claude
		"claude,,claude":    "claude",
		"claude,gemini":     "mixed",
		"antigravity,codex": "mixed",
	} {
		var in []string
		if types != "" {
			in = strings.Split(types, ",")
		}
		assert.Equal(t, want, TeamProvider(in), "%q", types)
	}
	assert.Equal(t, "claude", TeamProvider(nil))
}
