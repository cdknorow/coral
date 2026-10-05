package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/tracking"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// analyticsSink is a fake PostHog. No test here can reach the real service.
type analyticsSink struct {
	mu     sync.Mutex
	events []map[string]any
	raw    []string
}

func captureAnalytics(t *testing.T) *analyticsSink {
	t.Helper()
	sink := &analyticsSink{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		sink.mu.Lock()
		sink.events = append(sink.events, m)
		sink.raw = append(sink.raw, string(body))
		sink.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	prevKey := config.PostHogKey
	config.PostHogKey = "phc_test_key"
	restore := tracking.ConfigureForTest(srv.URL, t.TempDir(), "install-under-test")
	t.Cleanup(func() {
		restore()
		srv.Close()
		config.PostHogKey = prevKey
	})
	return sink
}

func (s *analyticsSink) named(event string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, e := range s.events {
		if e["event"] == event {
			out = append(out, e["properties"].(map[string]any))
		}
	}
	return out
}

func (s *analyticsSink) wait(t *testing.T, event string, n int) []map[string]any {
	t.Helper()
	require.Eventually(t, func() bool { return len(s.named(event)) >= n }, 3*time.Second, 5*time.Millisecond, "waiting for %d %s event(s)", n, event)
	return s.named(event)
}

func (s *analyticsSink) none(t *testing.T, event string) {
	t.Helper()
	time.Sleep(60 * time.Millisecond) // let any stray async send land
	assert.Empty(t, s.named(event), "%s must not be sent", event)
}

func (s *analyticsSink) allRaw() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.raw, "\n")
}

func postLaunch(t *testing.T, base, path string, body map[string]any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(base+path, "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestLaunchEmitsCorrelatedRequestedAndResultEvents(t *testing.T) {
	sink := captureAnalytics(t)
	server, _, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))

	require.Equal(t, 200, postLaunch(t, server.URL, "/api/sessions/launch", map[string]any{"working_dir": t.TempDir(), "agent_type": "claude"}))
	requested := sink.wait(t, tracking.EventLaunchRequested, 1)[0]
	result := sink.wait(t, tracking.EventLaunchResult, 1)[0]
	sink.wait(t, tracking.EventSessionLaunched, 1)
	assert.Equal(t, requested["attempt_id"], result["attempt_id"])
	assert.NotEmpty(t, result["attempt_id"])
	assert.Equal(t, "agent", result["kind"])
	assert.Equal(t, "claude", result["provider"])
	assert.Equal(t, "success", result["outcome"])
	assert.NotContains(t, result, "failure_category")
	assert.Equal(t, float64(1), result["started_agents"])
	assert.Equal(t, float64(0), result["failed_agents"])
	assert.Contains(t, result, "duration_ms")
	assert.Equal(t, float64(tracking.SchemaVersion), result["schema_version"])
	assert.Equal(t, tracking.RunID(), result["run_id"])
}

func TestLaunchFailuresUseControlledCategoriesAndNoRawText(t *testing.T) {
	sink := captureAnalytics(t)
	server, _, terminal, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))

	// Request errors.
	require.Equal(t, 400, postLaunch(t, server.URL, "/api/sessions/launch", map[string]any{"agent_type": "claude"}))
	require.Equal(t, 400, postLaunch(t, server.URL, "/api/sessions/launch", map[string]any{"working_dir": t.TempDir(), "agent_type": "no-such-agent"}))
	results := sink.wait(t, tracking.EventLaunchResult, 2)
	for _, r := range results {
		assert.Equal(t, "failure", r["outcome"])
		assert.Equal(t, "invalid_request", r["failure_category"])
		assert.Equal(t, float64(0), r["started_agents"])
	}

	// A terminal failure carrying a path and secret-looking text.
	terminal.mu.Lock()
	terminal.sendErr = errors.New("write /Users/alice/private/key.pem: permission denied for token abc123")
	terminal.mu.Unlock()
	require.Equal(t, 500, postLaunch(t, server.URL, "/api/sessions/launch", map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "display_name": "Alice's Secret Agent"}))
	results = sink.wait(t, tracking.EventLaunchResult, 3)
	last := results[len(results)-1]
	if last["failure_category"] == nil { // events can land out of order
		for _, r := range results {
			if r["failure_category"] != "invalid_request" {
				last = r
			}
		}
	}
	assert.Equal(t, "failure", last["outcome"])
	assert.Equal(t, "spawn_failed", last["failure_category"])
	sink.none(t, tracking.EventSessionLaunched)
	raw := sink.allRaw()
	for _, leaked := range []string{"/Users/alice", "abc123", "permission denied", "Secret Agent", "working_dir"} {
		assert.NotContains(t, raw, leaked, "analytics must never carry raw errors, paths or names")
	}
}

func TestTeamLaunchCountsStartedAndFailedMembersSeparately(t *testing.T) {
	sink := captureAnalytics(t)
	server, _, terminal, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	body := func() map[string]any {
		return map[string]any{
			"board_name": "tm", "working_dir": t.TempDir(), "agent_type": "claude",
			"agents": []map[string]any{{"name": "A"}, {"name": "B", "agent_type": "codex"}},
		}
	}

	// All members start: success, mixed providers, counts of started agents.
	require.Equal(t, 200, postLaunch(t, server.URL, "/api/sessions/launch-team", body()))
	result := sink.wait(t, tracking.EventLaunchResult, 1)[0]
	assert.Equal(t, "team", result["kind"])
	assert.Equal(t, "mixed", result["provider"])
	assert.Equal(t, "success", result["outcome"])
	assert.Equal(t, float64(2), result["requested_agents"])
	assert.Equal(t, float64(2), result["started_agents"])
	team := sink.wait(t, tracking.EventTeamLaunched, 1)[0]
	assert.Equal(t, float64(2), team["agent_count"])
	assert.Equal(t, float64(0), team["failed_agents"])
	sink.wait(t, tracking.EventFirstTeamLaunched, 1)

	// Every member fails: the HTTP call still returns 200 with per-member
	// errors, but nothing counts as a launched team.
	terminal.mu.Lock()
	terminal.sendErr = errors.New("boom at /Users/alice/x")
	terminal.mu.Unlock()
	b := body()
	b["board_name"] = "tm2"
	require.Equal(t, 200, postLaunch(t, server.URL, "/api/sessions/launch-team", b))
	results := sink.wait(t, tracking.EventLaunchResult, 2)
	var failedTeam map[string]any
	for _, r := range results {
		if r["outcome"] == "failure" {
			failedTeam = r
		}
	}
	require.NotNil(t, failedTeam)
	assert.Equal(t, float64(0), failedTeam["started_agents"])
	assert.Equal(t, float64(2), failedTeam["failed_agents"])
	assert.Equal(t, "spawn_failed", failedTeam["failure_category"])
	time.Sleep(60 * time.Millisecond)
	assert.Len(t, sink.named(tracking.EventTeamLaunched), 1, "a team that started nobody is not team_launched")
	assert.Len(t, sink.named(tracking.EventFirstTeamLaunched), 1)
	assert.NotContains(t, sink.allRaw(), "/Users/alice")
}

func TestTeamLaunchRequestErrorsAreFailures(t *testing.T) {
	sink := captureAnalytics(t)
	server, _, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	require.Equal(t, 400, postLaunch(t, server.URL, "/api/sessions/launch-team", map[string]any{"working_dir": "/tmp", "agents": []map[string]any{{"name": "dev"}}}))
	r := sink.wait(t, tracking.EventLaunchResult, 1)[0]
	assert.Equal(t, "failure", r["outcome"])
	assert.Equal(t, "invalid_request", r["failure_category"])
	assert.Equal(t, float64(1), r["failed_agents"])
	sink.none(t, tracking.EventTeamLaunched)
}

func TestClassifyLaunchErrorReturnsOnlyControlledCategories(t *testing.T) {
	allowed := map[string]bool{"provider_unavailable": true, "backend_unavailable": true, "workdir_unavailable": true, "spawn_failed": true, "internal": true}
	for msg, want := range map[string]string{
		"claude CLI not found. Install it: npm i -g x":        "provider_unavailable",
		"tmux is required to launch agents but was not found": "backend_unavailable",
		"chdir /Users/a/b: no such file or directory":         "workdir_unavailable",
		"directory not found: /Users/a/missing":               "workdir_unavailable",
		"pty spawn failed: fork/exec x":                       "spawn_failed",
		"tmux new-session failed: exit 1":                     "spawn_failed",
		"launch command delivery to t failed: x":              "spawn_failed",
		"something unexpected /secret/path":                   "internal",
	} {
		got := classifyLaunchError(errors.New(msg))
		assert.Equal(t, want, got, msg)
		assert.True(t, allowed[got], got)
	}
	assert.Equal(t, "", classifyLaunchError(nil))
}

func TestLaunchLabelsTheClaudeFallbackAndSkippedTeamMembers(t *testing.T) {
	sink := captureAnalytics(t)
	server, _, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))

	// No agent_type and no configured default: launchSession uses Claude.
	require.Equal(t, 200, postLaunch(t, server.URL, "/api/sessions/launch", map[string]any{"working_dir": t.TempDir()}))
	assert.Equal(t, "claude", sink.wait(t, tracking.EventLaunchResult, 1)[0]["provider"])

	// A nonexistent directory is a controlled workdir_unavailable failure.
	require.Equal(t, 500, postLaunch(t, server.URL, "/api/sessions/launch", map[string]any{"working_dir": "/definitely/not/a/real/dir", "agent_type": "claude"}))
	var dirFail map[string]any
	require.Eventually(t, func() bool {
		for _, r := range sink.named(tracking.EventLaunchResult) {
			if r["outcome"] == "failure" {
				dirFail = r
				return true
			}
		}
		return false
	}, 3*time.Second, 5*time.Millisecond)
	assert.Equal(t, "workdir_unavailable", dirFail["failure_category"])
	assert.NotContains(t, sink.allRaw(), "definitely/not")
}

func TestTeamProviderIsMixedOnlyWhenEffectiveProvidersDiffer(t *testing.T) {
	sink := captureAnalytics(t)
	server, _, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	launchTeam := func(name string, teamType string, agents []map[string]any) map[string]any {
		body := map[string]any{"board_name": name, "working_dir": t.TempDir(), "agents": agents}
		if teamType != "" {
			body["agent_type"] = teamType
		}
		require.Equal(t, 200, postLaunch(t, server.URL, "/api/sessions/launch-team", body))
		return nil
	}
	providers := func() []any {
		var out []any
		for _, r := range sink.named(tracking.EventLaunchResult) {
			out = append(out, r["provider"])
		}
		return out
	}
	// All members explicitly codex under an empty team default: one provider.
	launchTeam("t-codex", "", []map[string]any{{"name": "A", "agent_type": "codex"}, {"name": "B", "agent_type": "codex"}})
	sink.wait(t, tracking.EventLaunchResult, 1)
	// No types at all: the Claude fallback.
	launchTeam("t-default", "", []map[string]any{{"name": "C"}, {"name": "D"}})
	sink.wait(t, tracking.EventLaunchResult, 2)
	// Team type shared by all members.
	launchTeam("t-gem", "gemini", []map[string]any{{"name": "E"}, {"name": "F", "agent_type": "gemini"}})
	sink.wait(t, tracking.EventLaunchResult, 3)
	// Genuinely mixed: one codex among Claude-fallback members.
	launchTeam("t-mixed", "", []map[string]any{{"name": "G"}, {"name": "H", "agent_type": "codex"}})
	sink.wait(t, tracking.EventLaunchResult, 4)
	assert.ElementsMatch(t, []any{"codex", "claude", "gemini", "mixed"}, providers())
}

// Unnamed members are skipped by the launch loop (product behaviour is
// unchanged); the analytics must account for them without claiming starts.
func TestTeamLaunchAccountsForSkippedUnnamedMembers(t *testing.T) {
	sink := captureAnalytics(t)
	server, _, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	require.Equal(t, 200, postLaunch(t, server.URL, "/api/sessions/launch-team", map[string]any{
		"board_name": "skip", "working_dir": t.TempDir(), "agent_type": "claude",
		"agents": []map[string]any{{"name": "Real"}, {"name": ""}, {"prompt": "no name"}},
	}))
	r := sink.wait(t, tracking.EventLaunchResult, 1)[0]
	assert.Equal(t, float64(3), r["requested_agents"])
	assert.Equal(t, float64(1), r["started_agents"])
	assert.Equal(t, float64(2), r["failed_agents"], "skipped members are failures, never starts")
	assert.Equal(t, "partial", r["outcome"])
	assert.Equal(t, "invalid_request", r["failure_category"])
	team := sink.wait(t, tracking.EventTeamLaunched, 1)[0]
	assert.Equal(t, float64(1), team["agent_count"])
	assert.Equal(t, float64(3), team["requested_agents"])
	assert.Equal(t, float64(2), team["failed_agents"])

	// A team of only unnamed members starts nobody and is a failure.
	require.Equal(t, 200, postLaunch(t, server.URL, "/api/sessions/launch-team", map[string]any{
		"board_name": "skip2", "working_dir": t.TempDir(), "agents": []map[string]any{{"name": ""}},
	}))
	var allSkipped map[string]any
	require.Eventually(t, func() bool {
		for _, res := range sink.named(tracking.EventLaunchResult) {
			if res["outcome"] == "failure" {
				allSkipped = res
				return true
			}
		}
		return false
	}, 3*time.Second, 5*time.Millisecond)
	assert.Equal(t, float64(0), allSkipped["started_agents"])
	assert.Equal(t, float64(1), allSkipped["failed_agents"])
	assert.Len(t, sink.named(tracking.EventTeamLaunched), 1, "no team_launched for a team that started nobody")
}
