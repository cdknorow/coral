package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

const (
	subRouteMainA = "00000000-0000-0000-0000-0000000000a1"
	subRouteMainB = "00000000-0000-0000-0000-0000000000b2"
)

func strp(s string) *string { return &s }

func getSubagents(t *testing.T, url string) []map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotNil(t, out, "must be a JSON array, never null: the UI iterates it")
	return out
}

func setupSubagentRoute(t *testing.T) (string, *store.SubagentStore, *store.SessionStore) {
	t.Helper()
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	for _, sid := range []string{subRouteMainA, subRouteMainB} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
			SessionID: sid, AgentType: "claude", AgentName: "coral-go", WorkingDir: "/repo"}))
	}
	return server.URL + "/api/sessions/live/coral-go/subagents", h.subagents, ss
}

func TestListSubagents_ReturnsSpendAndStatusForOneMainAgent(t *testing.T) {
	url, st, _ := setupSubagentRoute(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertSubagent(ctx, &store.Subagent{SessionID: subRouteMainA, SubagentID: "done",
		SubagentType: strp("Explore"), Description: strp("Map the UI"), Model: strp("claude-opus-5"),
		APICalls: 25, InputTokens: 50, OutputTokens: 18854, CacheReadTokens: 2269410, CacheWriteTokens: 138794,
		CostUSD: 7.42, Finished: true, StartedAt: strp("2026-09-17T02:10:18Z")}))
	require.NoError(t, st.UpsertSubagent(ctx, &store.Subagent{SessionID: subRouteMainA, SubagentID: "running",
		SubagentType: strp("Plan"), Description: strp("Design it"), CostUSD: 0.5, StartedAt: strp("2026-09-17T02:20:00Z")}))
	require.NoError(t, st.UpsertSubagent(ctx, &store.Subagent{SessionID: subRouteMainB, SubagentID: "someone-elses"}))

	got := getSubagents(t, url+"?session_id="+subRouteMainA)
	require.Len(t, got, 2, "only this main agent's subagents, although both agents share the name coral-go")

	done, running := got[0], got[1]
	assert.Equal(t, "done", done["subagent_id"])
	assert.Equal(t, subRouteMainA, done["session_id"])
	assert.Equal(t, "completed", done["status"])
	assert.Equal(t, true, done["finished"])
	assert.Equal(t, "Explore", done["subagent_type"])
	assert.Equal(t, "Map the UI", done["description"])
	assert.Equal(t, "claude-opus-5", done["model"])
	assert.EqualValues(t, 25, done["api_calls"])
	assert.EqualValues(t, 18854, done["output_tokens"])
	assert.EqualValues(t, 2269410, done["cache_read_tokens"])
	assert.InDelta(t, 7.42, done["cost_usd"], 1e-9)

	assert.Equal(t, "running", running["subagent_id"])
	assert.Equal(t, "in_progress", running["status"])
}

// A subagent lives inside its main agent's process. If that agent is stopped
// mid-run the subagent can never finish, and must not spin forever in the UI.
func TestListSubagents_UnfinishedSubagentOfStoppedMainAgentIsNotInProgress(t *testing.T) {
	url, st, ss := setupSubagentRoute(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertSubagent(ctx, &store.Subagent{SessionID: subRouteMainA, SubagentID: "interrupted"}))
	require.NoError(t, st.UpsertSubagent(ctx, &store.Subagent{SessionID: subRouteMainA, SubagentID: "done", Finished: true}))
	require.NoError(t, ss.UnregisterLiveSession(ctx, subRouteMainA))

	byID := map[string]string{}
	for _, sa := range getSubagents(t, url+"?session_id="+subRouteMainA) {
		byID[sa["subagent_id"].(string)] = sa["status"].(string)
	}
	assert.Equal(t, "skipped", byID["interrupted"])
	assert.Equal(t, "completed", byID["done"], "finished work stays completed")
}

func TestListSubagents_EmptyCases(t *testing.T) {
	url, _, _ := setupSubagentRoute(t)
	assert.Empty(t, getSubagents(t, url+"?session_id="+subRouteMainA), "main agent with no subagents")
	assert.Empty(t, getSubagents(t, url+"?session_id=no-such-session"))
	assert.Empty(t, getSubagents(t, url), "no session_id")
}

func TestSubagentStatus(t *testing.T) {
	assert.Equal(t, "completed", subagentStatus(store.Subagent{Finished: true}, false))
	assert.Equal(t, "completed", subagentStatus(store.Subagent{Finished: true}, true))
	assert.Equal(t, "in_progress", subagentStatus(store.Subagent{}, false))
	assert.Equal(t, "skipped", subagentStatus(store.Subagent{}, true))
}

// ── Detail endpoint ──────────────────────────────────────────

func getJSON(t *testing.T, url string, wantStatus int) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, wantStatus, resp.StatusCode)
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func writeSubagentTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-s1.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return path
}

func TestGetSubagent_ReturnsStatsWithPromptAndResult(t *testing.T) {
	url, st, _ := setupSubagentRoute(t)
	path := writeSubagentTranscript(t,
		`{"type":"user","message":{"role":"user","content":"Map the activity tab."}}`,
		`{"type":"assistant","message":{"id":"m1","stop_reason":"end_turn","content":[{"type":"text","text":"It lives in activity.js."}]}}`)
	require.NoError(t, st.UpsertSubagent(context.Background(), &store.Subagent{SessionID: subRouteMainA, SubagentID: "s1",
		SubagentType: strp("Explore"), Description: strp("Map activity tab"), Model: strp("claude-opus-5"),
		APICalls: 25, OutputTokens: 900, CostUSD: 3.92, Finished: true, TranscriptPath: &path}))

	got := getJSON(t, url+"/s1?session_id="+subRouteMainA, http.StatusOK)
	assert.Equal(t, "s1", got["subagent_id"])
	assert.Equal(t, "completed", got["status"])
	assert.Equal(t, "Explore", got["subagent_type"])
	assert.EqualValues(t, 25, got["api_calls"])
	assert.InDelta(t, 3.92, got["cost_usd"], 1e-9)
	assert.Equal(t, "Map the activity tab.", got["prompt"])
	assert.Equal(t, "It lives in activity.js.", got["result"])
	assert.Equal(t, true, got["conversation_available"])
	assert.Equal(t, false, got["truncated"])
	for k := range got {
		assert.NotContains(t, k, "transcript", "the server-side path is not exposed")
	}
}

func TestGetSubagent_RunningSubagentHasPromptButNoResult(t *testing.T) {
	url, st, _ := setupSubagentRoute(t)
	path := writeSubagentTranscript(t,
		`{"type":"user","message":{"role":"user","content":"Do the thing."}}`,
		`{"type":"assistant","message":{"id":"m1","stop_reason":"tool_use","content":[{"type":"text","text":"Looking..."}]}}`)
	require.NoError(t, st.UpsertSubagent(context.Background(), &store.Subagent{SessionID: subRouteMainA, SubagentID: "s1", TranscriptPath: &path}))

	got := getJSON(t, url+"/s1?session_id="+subRouteMainA, http.StatusOK)
	assert.Equal(t, "in_progress", got["status"])
	assert.Equal(t, "Do the thing.", got["prompt"])
	assert.Equal(t, "", got["result"])
}

// Claude Code prunes old transcripts. The stored spend must still be viewable.
func TestGetSubagent_MissingTranscriptStillReturnsStats(t *testing.T) {
	url, st, _ := setupSubagentRoute(t)
	gone := filepath.Join(t.TempDir(), "agent-pruned.jsonl")
	require.NoError(t, st.UpsertSubagent(context.Background(), &store.Subagent{SessionID: subRouteMainA, SubagentID: "pruned",
		CostUSD: 1.5, Finished: true, TranscriptPath: &gone}))
	require.NoError(t, st.UpsertSubagent(context.Background(), &store.Subagent{SessionID: subRouteMainA, SubagentID: "nopath", CostUSD: 2.5}))

	for id, cost := range map[string]float64{"pruned": 1.5, "nopath": 2.5} {
		got := getJSON(t, url+"/"+id+"?session_id="+subRouteMainA, http.StatusOK)
		assert.InDelta(t, cost, got["cost_usd"], 1e-9)
		assert.Equal(t, false, got["conversation_available"])
		assert.Equal(t, "", got["prompt"])
	}
}

func TestGetSubagent_NotFoundAndScoping(t *testing.T) {
	url, st, _ := setupSubagentRoute(t)
	require.NoError(t, st.UpsertSubagent(context.Background(), &store.Subagent{SessionID: subRouteMainA, SubagentID: "s1"}))

	getJSON(t, url+"/nope?session_id="+subRouteMainA, http.StatusNotFound)
	getJSON(t, url+"/s1?session_id="+subRouteMainB, http.StatusNotFound) // another main agent's subagent is not reachable
	getJSON(t, url+"/s1", http.StatusBadRequest)
}
