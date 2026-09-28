package routes

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTeamAvailabilityQueuesAndRuntime(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	team := "routing"
	ids := map[string]string{}
	for _, name := range []string{"free", "board-busy", "personal-busy", "queued", "working", "input", "sleeping", "offline", "unknown"} {
		sid := uuid.NewString()
		ids[name] = sid
		s := &store.LiveSession{SessionID: sid, AgentName: name, AgentType: "claude", BoardName: &team, WorkingDir: "/tmp"}
		if name == "sleeping" {
			s.IsSleeping = 1
		}
		require.NoError(t, ss.RegisterLiveSession(ctx, s))
		_, err := h.bs.Subscribe(ctx, team, name, "Developer", "claude-"+sid, nil, nil, "all")
		require.NoError(t, err)
		if name != "offline" {
			terminal.addSession("claude-"+sid, "/tmp")
		}
		if name != "unknown" {
			typ, summary := "stop", ""
			if name == "working" {
				typ = "tool_use"
			}
			if name == "input" {
				typ = "notification"
				summary = "Claude needs your approval"
			}
			_, err = h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: name, SessionID: &sid, EventType: typ, Summary: summary})
			require.NoError(t, err)
		}
	}
	_, err := h.bs.CreateTask(ctx, team, "Board implementation", "", "high", "lead", "board-busy")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, team, "board-busy")
	require.NoError(t, err)
	sid := ids["personal-busy"]
	_, err = h.ts.CreateAgentTask(ctx, "personal-busy", "Personal implementation", &sid, nil)
	require.NoError(t, err)
	_, err = h.ts.ClaimNextAgentTask(ctx, "personal-busy", &sid)
	require.NoError(t, err)
	_, err = h.bs.CreateTask(ctx, team, "Assigned next", "", "medium", "lead", "queued")
	require.NoError(t, err)
	_, err = h.bs.CreateTask(ctx, team, "Open pool", "", "medium", "lead")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, "different-team", "Other", "", "claude-other", nil, nil, "all")
	require.NoError(t, err)
	r := chi.NewRouter()
	r.Get("/api/teams/detail/{name}/availability", h.TeamAvailability)
	r.Get("/api/board/{project}/status", h.TeamAvailability)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/teams/detail/routing/availability", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	var result struct {
		Agents     []availableAgent   `json:"agents"`
		Unassigned []availabilityTask `json:"unassigned_tasks"`
		Summary    map[string]int     `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	expected := map[string]string{"free": "available", "board-busy": "task_idle", "personal-busy": "task_idle", "queued": "queued", "working": "busy", "input": "needs_input", "sleeping": "sleeping", "offline": "offline", "unknown": "unknown"}
	require.Len(t, result.Agents, len(expected))
	for _, a := range result.Agents {
		require.Equal(t, expected[a.Name], a.Availability, a.Name)
		require.Equal(t, a.Name == "free", a.Available, a.Name)
	}
	require.Equal(t, 1, result.Summary["available"])
	require.Len(t, result.Unassigned, 1)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	other := httptest.NewRecorder()
	r.ServeHTTP(other, httptest.NewRequest("GET", "/api/board/routing/status", nil))
	require.Equal(t, 200, other.Code)
	var legacy, canonical map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &legacy))
	require.NoError(t, json.Unmarshal(other.Body.Bytes(), &canonical))
	delete(legacy, "observed_at")
	delete(canonical, "observed_at")
	require.Equal(t, legacy, canonical)
	require.Equal(t, "routing", canonical["board"])

}

func TestAvailabilityBlockedAndDraftDoNotReserveAgent(t *testing.T) {
	a := availableAgent{Tasks: []availabilityTask{{Status: "blocked"}, {Status: "draft"}}}
	classifyAvailability(&a, store.LiveSession{AgentType: "claude"}, true, true, SessionState{AwaitingUser: true}, true)
	require.True(t, a.Available)
	a.Tasks = append(a.Tasks, availabilityTask{Status: "in_progress"})
	classifyAvailability(&a, store.LiveSession{AgentType: "claude"}, true, true, SessionState{AwaitingUser: true}, true)
	require.Equal(t, "task_idle", a.Availability)
}

func TestAvailabilityTranscriptFallbackRespectsNewerHooks(t *testing.T) {
	at, err := time.Parse(time.RFC3339, "2026-09-26T16:29:51Z")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, hookTime string
		free           bool
	}{
		{"missing hook", "", true},
		{"older activity", "2026-09-26T16:28:00Z", true},
		{"newer approval", "2026-09-26T16:30:00Z", false},
		{"same second approval", "2026-09-26T16:29:51Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := SessionStateInput{}
			events := []store.AgentEvent{}
			if tc.hookTime != "" {
				events = append(events, store.AgentEvent{CreatedAt: tc.hookTime})
				input.Events = append(input.Events, StateEvent{Type: "notification", Summary: "Claude needs your approval"})
			}
			mergeTranscriptTurnEvent(&input, events, "stop", at)
			a := availableAgent{}
			classifyAvailability(&a, store.LiveSession{AgentType: "codex"}, true, true, DeriveSessionState(input), len(input.Events) > 0)
			require.Equal(t, tc.free, a.Available)
		})
	}
}

func TestTeamAvailabilityCodexWithoutHooks(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	dir := filepath.Join(home, "sessions", "2026", "09", "26")
	require.NoError(t, os.MkdirAll(dir, 0700))
	sid := uuid.NewString()
	team := "music-team"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentName: "music", AgentType: "codex", BoardName: &team, WorkingDir: "/tmp"}))
	terminal.addSession("codex-"+sid, "/tmp")
	_, err := h.bs.Subscribe(context.Background(), team, "Music SFX director", "Music", "codex-"+sid, nil, nil, "all")
	require.NoError(t, err)
	path := filepath.Join(dir, "rollout-"+sid+".jsonl")
	data := `{"timestamp":"2026-09-26T16:29:51.825Z","type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(data), 0600))
	router := chi.NewRouter()
	router.Get("/api/teams/detail/{name}/availability", h.TeamAvailability)
	check := func(want string) {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/teams/detail/music-team/availability", nil))
		require.Equal(t, 200, w.Code)
		var out struct {
			Agents []availableAgent `json:"agents"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		require.Len(t, out.Agents, 1)
		require.Equal(t, want, out.Agents[0].Availability)
	}
	check("available")
	data += `{"timestamp":"2026-09-26T16:30:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(data), 0600))
	check("busy")
}

func TestTeamAvailabilityAgyWithoutHooks(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	home := t.TempDir()
	t.Setenv("ANTIGRAVITY_DATA_DIR", home)
	sid := uuid.NewString()
	convID := uuid.NewString()
	dir := filepath.Join(home, "brain", convID, ".system_generated", "logs")
	require.NoError(t, os.MkdirAll(dir, 0700))
	team := "seo-team"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentName: "seo", AgentType: "agy", BoardName: &team, WorkingDir: "/tmp"}))
	terminal.addSession("agy-"+sid, "/tmp")
	_, err := h.bs.Subscribe(context.Background(), team, "SEO Strategist", "SEO", "agy-"+sid, nil, nil, "all")
	require.NoError(t, err)

	path := filepath.Join(dir, "transcript.jsonl")
	// Step 0: prompt with embedded CORAL_SESSION_ID so resolveAgyTranscript finds it
	data := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-28T03:00:00Z","content":"CORAL_SESSION_ID: ` + sid + `"}` + "\n"
	// Step 1: PLANNER_RESPONSE with no tools -> turn ended
	data += `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-28T03:00:01Z","tool_calls":[]}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(data), 0600))

	router := chi.NewRouter()
	router.Get("/api/teams/detail/{name}/availability", h.TeamAvailability)
	check := func(want string) {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/teams/detail/seo-team/availability", nil))
		require.Equal(t, 200, w.Code)
		var out struct {
			Agents []availableAgent `json:"agents"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		require.Len(t, out.Agents, 1)
		require.Equal(t, want, out.Agents[0].Availability)
	}
	check("available")

	// Model starts working on a tool
	data += `{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-28T03:00:05Z","tool_calls":[{"name":"run_command"}]}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(data), 0600))
	check("busy")

	// Model asks user a question -> needs_input
	data += `{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-28T03:00:08Z","tool_calls":[{"name":"ask_question"}]}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(data), 0600))
	check("needs_input")
}

func TestTeamAvailabilityWithActiveWait(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	team := "wait-team"
	sid := uuid.NewString()
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  sid,
		AgentName:  "Worker",
		AgentType:  "claude",
		BoardName:  &team,
		WorkingDir: "/tmp",
	}))
	terminal.addSession("claude-"+sid, "/tmp")
	_, err := h.bs.Subscribe(ctx, team, "Worker", "Developer", "claude-"+sid, nil, nil, "all")
	require.NoError(t, err)

	// Add an in-progress task for the worker
	_, err = h.bs.CreateTask(ctx, team, "Feature work", "", "high", "lead", "Worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, team, "Worker")
	require.NoError(t, err)

	// Register a wait for Orchestrator
	_, err = h.bs.RegisterWait(ctx, team, "Worker", "claude-"+sid, "message", "Orchestrator", "waiting for review", 10*time.Minute)
	require.NoError(t, err)

	router := chi.NewRouter()
	router.Get("/api/teams/detail/{name}/availability", h.TeamAvailability)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/teams/detail/wait-team/availability", nil))
	require.Equal(t, 200, w.Code)

	var result struct {
		Agents  []availableAgent `json:"agents"`
		Summary map[string]int   `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Len(t, result.Agents, 1)
	require.Equal(t, "waiting", result.Agents[0].Availability)
	require.False(t, result.Agents[0].Available)
	require.NotNil(t, result.Agents[0].WaitingOn)
	require.Equal(t, "message", result.Agents[0].WaitingOn.WaitType)
	require.Equal(t, "Orchestrator", result.Agents[0].WaitingOn.TargetID)
	require.Equal(t, "Waiting for message from 'Orchestrator' (waiting for review)", result.Agents[0].Reason)
	require.Equal(t, 1, result.Summary["waiting"])
}

