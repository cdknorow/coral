package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3518: distilling through Coral Hub. The POST (Launch tier) takes no body or query, only
// for a team that exists, one run per team at a time and at most one a minute; the status
// (View tier) has fixed keys, and its text is bounded, one line, with no file paths.
func TestDistillKnowledgeRequestRules(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	db, err := storetest.Open(t, t.TempDir()+"/teams.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h.SetTeamStore(store.NewTeamStore(db))
	ctx := context.Background()
	team := "team-3518"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "aaaaaaaa-3518-0000-0000-000000000001", AgentType: "claude", AgentName: "a", WorkingDir: "/w", BoardName: &team}))

	post := func(name, query, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/teams/detail/"+name+"/distill-knowledge"+query, strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", name)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		h.DistillKnowledge(rr, req)
		return rr
	}
	set := func(j *knowledgeJob) {
		knowledgeJobsMu.Lock()
		if j == nil {
			delete(knowledgeJobs, team)
		} else {
			knowledgeJobs[team] = j
		}
		knowledgeJobsMu.Unlock()
	}
	t.Cleanup(func() { set(nil) })

	assert.Equal(t, http.StatusBadRequest, post(team, "", `{"x":1}`).Code, "a body is refused")
	assert.Equal(t, http.StatusBadRequest, post(team, "?model=opus", "").Code, "a query is refused")
	for _, bad := range []string{"..", ".", "a/b", `a\b`, strings.Repeat("x", 129)} {
		assert.Equal(t, http.StatusBadRequest, post(bad, "", "").Code, "invalid name %q", bad)
	}
	assert.Equal(t, http.StatusNotFound, post("no-such-team-3518", "", "").Code, "a team that doesn't exist never makes a job")
	knowledgeJobsMu.Lock()
	_, made := knowledgeJobs["no-such-team-3518"]
	knowledgeJobsMu.Unlock()
	assert.False(t, made)

	// A run in progress: a second request gets it, nothing new starts.
	running := &knowledgeJob{TeamName: team, Status: "running", StartedAt: time.Now().UTC().Add(-5 * time.Second).Format(time.RFC3339)}
	set(running)
	rr := post(team, "", "")
	assert.Equal(t, http.StatusOK, rr.Code)
	knowledgeJobsMu.Lock()
	same := knowledgeJobs[team] == running
	knowledgeJobsMu.Unlock()
	assert.True(t, same, "the running job is returned, not replaced")

	// A run that started under a minute ago: 429 as an action limit, with Retry-After.
	set(&knowledgeJob{TeamName: team, Status: "complete", StartedAt: time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339)})
	rr = post(team, "", "")
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Equal(t, "action", rr.Header().Get("X-Coral-Limit"))
	assert.NotEmpty(t, rr.Header().Get("Retry-After"))
}

func TestKnowledgeStatusHasFixedBoundedFields(t *testing.T) {
	long := strings.Repeat("e", 500)
	var failed []background.DistillFailure
	for i := 0; i < 30; i++ {
		failed = append(failed, background.DistillFailure{Agent: strings.Repeat("n", 100), Error: "claude CLI failed: exit status 1: open /home/someone/.claude/x.json: no such file"})
	}
	knowledgeJobsMu.Lock()
	knowledgeJobs["bounded-3518"] = &knowledgeJob{TeamName: "bounded-3518", Status: "failed",
		Error:              "knowledge distilled but failed to write: mkdir ~/notes/k:\npermission denied and/or " + long,
		Progress:           background.DistillProgress{Phase: "agents", Agent: strings.Repeat("a", 100), Error: "see /tmp/transcript.jsonl"},
		FailedAgents:       failed,
		CollaborationError: `read './rel/path.md' and notes/report.md failed; signed in as someone.else@example.test, see C:\Users\someone\x.log or \\server\share\y`}
	knowledgeJobsMu.Unlock()
	t.Cleanup(func() { knowledgeJobsMu.Lock(); delete(knowledgeJobs, "bounded-3518"); knowledgeJobsMu.Unlock() })

	m := getKnowledgeStatus(t, "bounded-3518")
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"status", "progress", "error", "failed_agents", "collaboration_error"}, keys, "fixed keys only (no team_name)")
	raw, _ := json.Marshal(m)
	for _, leak := range []string{"/home/someone", "/tmp/transcript", "~/notes", "./rel/path", "someone.else@example.test", "notes/report", `C:\\Users`, `\\\\server`, "Users", "share"} {
		assert.NotContains(t, string(raw), leak, "no file paths")
	}
	errText := m["error"].(string)
	assert.LessOrEqual(t, len([]rune(errText)), knowledgeMaxText+1)
	assert.NotContains(t, errText, "\n", "one line")
	assert.Contains(t, errText, "and/or", "an ordinary word with one slash and no extension survives")
	assert.Len(t, m["failed_agents"], knowledgeMaxFailed, "failed agents are capped")
	first := m["failed_agents"].([]any)[0].(map[string]any)
	assert.LessOrEqual(t, len([]rune(first["agent"].(string))), knowledgeMaxName+1)
	assert.LessOrEqual(t, len([]rune(m["progress"].(map[string]any)["agent"].(string))), knowledgeMaxName+1)
}

// Through Coral Hub, the run's error texts are a fixed message: the free text stays on this
// machine (a path with a space or a name can slip past any scrubbing).
func TestKnowledgeStatusThroughTheHubHidesErrorText(t *testing.T) {
	knowledgeJobsMu.Lock()
	knowledgeJobs["hub-3518"] = &knowledgeJob{TeamName: "hub-3518", Status: "failed", Error: `open C:\Users\Jane Doe\x.log failed`,
		Progress:     background.DistillProgress{Phase: "agents", Error: "see /Users/Jane Doe/y"},
		FailedAgents: []background.DistillFailure{{Agent: "Dev", Error: "Jane Doe's transcript"}}, CollaborationError: "x"}
	knowledgeJobsMu.Unlock()
	t.Cleanup(func() { knowledgeJobsMu.Lock(); delete(knowledgeJobs, "hub-3518"); knowledgeJobsMu.Unlock() })

	get := func(hub bool) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/teams/detail/hub-3518/distill-knowledge", nil)
		if hub {
			req.Header.Set(tunnel.ViaHeader, "1")
		}
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", "hub-3518")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		(&SessionsHandler{}).GetKnowledgeStatus(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		var m map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &m))
		return m
	}
	hub := get(true)
	raw, _ := json.Marshal(hub)
	assert.NotContains(t, string(raw), "Doe", "nothing of the free text reaches a hub viewer")
	assert.Equal(t, knowledgeHubText, hub["error"])
	assert.Equal(t, knowledgeHubText, hub["collaboration_error"])
	assert.Equal(t, knowledgeHubText, hub["progress"].(map[string]any)["error"])
	assert.Equal(t, "Dev", hub["failed_agents"].([]any)[0].(map[string]any)["agent"], "the agent's name stays")
	assert.Equal(t, knowledgeHubText, hub["failed_agents"].([]any)[0].(map[string]any)["error"])
	local := get(false)
	assert.Contains(t, local["error"], "failed", "the dashboard on the machine keeps the scrubbed detail")
	assert.NotEqual(t, knowledgeHubText, local["error"])
}
