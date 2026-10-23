package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/store"
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
		_, err := h.bs.Subscribe(ctx, team, name, "Developer", "claude-"+sid, nil, "all")
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
	_, err = h.bs.Subscribe(ctx, "different-team", "Other", "", "claude-other", nil, "all")
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

func TestTeamAvailabilityActivityIsPrivateOnDemandForOrchestrator(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	team := "private-activity"
	sid := uuid.NewString()
	display := "Lead Coordinator"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: "lead", AgentType: "claude", DisplayName: &display, BoardName: &team, WorkingDir: "/tmp"}))
	_, err := h.bs.Subscribe(ctx, team, "Lead Coordinator", "Orchestrator", "claude-"+sid, nil, "all", true)
	require.NoError(t, err)
	task, err := h.bs.CreateTask(ctx, team, "Review queue", "", "medium", "Operator", "Lead Coordinator")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, team, "Lead Coordinator", task.ID)
	require.NoError(t, err)
	terminal.addSession("claude-"+sid, "/tmp")
	terminal.setOutput("claude-"+sid, "old\n\x1b[32mworking @worker\x1b[0m")
	r := chi.NewRouter()
	r.Get("/api/board/{project}/status", h.TeamAvailability)

	unauthorized := httptest.NewRecorder()
	r.ServeHTTP(unauthorized, httptest.NewRequest("GET", "/api/board/"+team+"/status?activity=1&subscriber_id=Worker", nil))
	require.Equal(t, 403, unauthorized.Code)

	private := httptest.NewRecorder()
	r.ServeHTTP(private, httptest.NewRequest("GET", "/api/board/"+team+"/status?activity=1&subscriber_id=Lead%20Coordinator", nil))
	require.Equal(t, 200, private.Code, private.Body.String())
	var result struct {
		Agents []availableAgent `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(private.Body.Bytes(), &result))
	require.Len(t, result.Agents, 1)
	assert.Equal(t, "working (at)worker", result.Agents[0].Activity)
	terminal.mu.Lock()
	assert.Equal(t, []string{"claude-" + sid}, terminal.captures)
	terminal.mu.Unlock()

	ordinary := httptest.NewRecorder()
	r.ServeHTTP(ordinary, httptest.NewRequest("GET", "/api/board/"+team+"/status", nil))
	require.Equal(t, 200, ordinary.Code)
	var ordinaryResult struct {
		Agents []availableAgent `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(ordinary.Body.Bytes(), &ordinaryResult))
	require.Len(t, ordinaryResult.Agents, 1)
	assert.Empty(t, ordinaryResult.Agents[0].Activity)
	terminal.mu.Lock()
	assert.Equal(t, []string{"claude-" + sid}, terminal.captures, "regular status must never capture terminal output")
	terminal.mu.Unlock()
}

func TestTeamAvailabilityActivityRejectsAnonymousWorkerAndCrossTeamCaller(t *testing.T) {
	_, h, _, _ := setupSessionsTestServer(t)
	ctx := context.Background()
	team := "private-activity-boundary"
	_, err := h.bs.Subscribe(ctx, team, "Lead Coordinator", "Orchestrator", "claude-lead", nil, "all", true)
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, team, "Worker", "Worker", "claude-worker", nil, "all")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, "other-team", "Other Orchestrator", "Orchestrator", "claude-other", nil, "all", true)
	require.NoError(t, err)
	r := chi.NewRouter()
	r.Get("/api/board/{project}/status", h.TeamAvailability)
	for name, caller := range map[string]string{"anonymous": "", "worker": "Worker", "cross-team": "Other Orchestrator"} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			path := "/api/board/" + team + "/status?activity=1"
			if caller != "" {
				path += "&subscriber_id=" + url.QueryEscape(caller)
			}
			r.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			require.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
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
	_, err := h.bs.Subscribe(context.Background(), team, "Music SFX director", "Music", "codex-"+sid, nil, "all")
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
	_, err := h.bs.Subscribe(context.Background(), team, "SEO Strategist", "SEO", "agy-"+sid, nil, "all")
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
	_, err := h.bs.Subscribe(ctx, team, "Worker", "Developer", "claude-"+sid, nil, "all")
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
	// #3520 (QA's bug bash F5): a wait alone is not a review. The task is in progress.
	require.Equal(t, "active", result.Agents[0].TaskState)
}

// #3520 (QA's bug bash F5): "awaiting-review" only when one of the agent's tasks is
// waiting for its completion review; a registered wait with no task says nothing.
func TestTeamAvailabilityAwaitingReviewNeedsATaskUnderReview(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	team := "review-state-team"
	sid := uuid.NewString()
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: "Worker", AgentType: "claude", BoardName: &team, WorkingDir: "/tmp"}))
	terminal.addSession("claude-"+sid, "/tmp")
	_, err := h.bs.Subscribe(ctx, team, "Lead", "Orchestrator", "lead-session", nil, "all")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, team, "Worker", "Developer", "claude-"+sid, nil, "all")
	require.NoError(t, err)
	_, err = h.bs.RegisterWait(ctx, team, "Worker", "claude-"+sid, "message", "Lead", "", 10*time.Minute)
	require.NoError(t, err)

	router := chi.NewRouter()
	router.Get("/api/teams/detail/{name}/availability", h.TeamAvailability)
	state := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/teams/detail/"+team+"/availability", nil))
		require.Equal(t, 200, w.Code)
		var result struct {
			Agents []availableAgent `json:"agents"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		for _, a := range result.Agents {
			if a.SubscriberID == "Worker" {
				return a.TaskState
			}
		}
		t.Fatal("no Worker in the answer")
		return ""
	}
	assert.Empty(t, state(), "a wait with no task under review is not awaiting-review")

	task, err := h.bs.CreateTask(ctx, team, "Build", "", "medium", "Lead", "Worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, team, "Worker", task.ID)
	require.NoError(t, err)
	_, err = h.bs.SubmitCompletionReview(ctx, team, task.ID, "Worker", "done", "success", "review pending", []board.TaskArtifact{{Name: "build", Content: "candidate"}})
	require.NoError(t, err)
	assert.Equal(t, "awaiting-review", state(), "its task is under review")
}

// The person is not a team member to be reminded (#6382, first-run report #6370 item 6): a team's Operator subscription shows no card, no "Offline" count and no Remind button;
// the agents beside it are listed as before.
func TestTeamAvailabilityDoesNotListTheOperator(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	team := "operator-team"
	sid := uuid.NewString()
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: "Worker", AgentType: "claude", BoardName: &team, WorkingDir: "/tmp"}))
	terminal.addSession("claude-"+sid, "/tmp")
	_, err := h.bs.Subscribe(ctx, team, "Worker", "Developer", "claude-"+sid, nil, "all")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, team, board.OperatorName, "Operator", "", nil, "all")
	require.NoError(t, err)

	router := chi.NewRouter()
	router.Get("/api/teams/detail/{name}/availability", h.TeamAvailability)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/teams/detail/operator-team/availability", nil))
	require.Equal(t, 200, w.Code)
	var result struct {
		Agents  []availableAgent `json:"agents"`
		Summary map[string]int   `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	var names []string
	for _, a := range result.Agents {
		names = append(names, a.Name)
	}
	require.Equal(t, []string{"Worker"}, names)
	require.Zero(t, result.Summary["offline"])
}

// #285: the status says whether the board is there (a subscriber, a message or an open task), so coral-board status can say
// "Board not found" for a name with nothing on it instead of an empty board's "No queue health issues".
func TestTeamAvailabilitySaysWhetherTheBoardExists(t *testing.T) {
	_, h, _, _ := setupSessionsTestServer(t)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "with-member", "QA", "QA", "claude-qa", nil, "all")
	require.NoError(t, err)
	_, err = h.bs.PostMessage(ctx, "with-message", "someone", "hello", nil)
	require.NoError(t, err)
	r := chi.NewRouter()
	r.Get("/api/board/{project}/status", h.TeamAvailability)
	for board, want := range map[string]bool{"with-member": true, "with-message": true, "nothing-here": false} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/board/"+board+"/status", nil))
		require.Equal(t, 200, w.Code, w.Body.String())
		var got struct {
			Exists *bool `json:"exists"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		require.NotNil(t, got.Exists, board)
		require.Equal(t, want, *got.Exists, board)
	}
}
