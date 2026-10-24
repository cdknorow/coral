package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #7087: the goal routes. A goal is board text: the Operator and an orchestrator write it, anyone reads it, a goal of another board looks like no goal, and nothing here starts, wakes or
// messages an agent.

func goalCall(t *testing.T, base string) func(method, path string, body any) (int, map[string]any) {
	t.Helper()
	return func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, base+path, rd)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
}

func goalsOf(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	raw, ok := out["goals"].([]any)
	require.True(t, ok, "%v", out)
	res := make([]map[string]any, len(raw))
	for i, g := range raw {
		res[i] = g.(map[string]any)
	}
	return res
}

func TestGoalRoutesEndToEnd(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	registerTaskPlanner(t, h, "team", "Orchestrator")
	call := goalCall(t, server.URL)

	code, g := call("POST", "/api/board/team/goals", map[string]any{"title": "Ship the beta", "outcome": "People can use it", "done_when": "Release is out", "subscriber_id": "Orchestrator", "driver": "Orchestrator"})
	require.Equal(t, http.StatusCreated, code, "%v", g)
	id := int64(g["id"].(float64))
	assert.Regexp(t, canonicalGoalUUID, g["goal_uid"])
	assert.Equal(t, "active", g["state"])
	assert.Equal(t, "Orchestrator", g["created_by"])
	assert.Equal(t, "Orchestrator", g["driver"])
	assert.Equal(t, map[string]any{"done": 0.0, "active": 0.0, "blocked": 0.0, "queued": 0.0, "total": 0.0}, g["rollup"])

	code, out := call("GET", "/api/board/team/goals", nil)
	require.Equal(t, 200, code)
	assert.Len(t, goalsOf(t, out), 1)
	code, g = call("GET", "/api/board/team/goals/"+itoa(id), nil)
	require.Equal(t, 200, code)
	assert.Equal(t, "Ship the beta", g["title"])

	// Keeping it true: blocked says on whom, and what is needed; leaving blocked clears them.
	code, out = call("PATCH", "/api/board/team/goals/"+itoa(id), map[string]any{"state": "blocked", "subscriber_id": "Orchestrator"})
	assert.Equal(t, 400, code)
	assert.Equal(t, "goal_blocked_on_required", out["code"])
	code, g = call("PATCH", "/api/board/team/goals/"+itoa(id), map[string]any{"state": "blocked", "blocked_on": "the operator", "decision": "Ship Friday?", "recommendation": "yes", "if_silent": "ship Monday", "subscriber_id": "Orchestrator"})
	require.Equal(t, 200, code, "%v", g)
	assert.Equal(t, "Ship Friday?", g["decision"])
	assert.Equal(t, "ship Monday", g["if_silent"])
	code, g = call("PATCH", "/api/board/team/goals/"+itoa(id), map[string]any{"state": "active", "driver": "", "subscriber_id": "Orchestrator"})
	require.Equal(t, 200, code)
	assert.Nil(t, g["decision"], "leaving blocked clears the decision fields")
	assert.Nil(t, g["driver"], "an empty driver clears it")

	// Tasks under the goal: made with goal_id, moved and unlinked with the task route, listed by goal, and counted in the rollup.
	code, t1 := call("POST", "/api/board/team/tasks", map[string]any{"title": "First", "subscriber_id": "Orchestrator", "goal_id": id})
	require.Equal(t, http.StatusCreated, code, "%v", t1)
	assert.EqualValues(t, id, t1["goal_id"])
	task2, err := h.bs.CreateTask(ctx, "team", "Second", "", "medium", "Orchestrator")
	require.NoError(t, err)
	code, moved := call("PUT", "/api/board/team/tasks/"+itoa(task2.ID)+"/goal", map[string]any{"goal_id": id, "subscriber_id": "Orchestrator"})
	require.Equal(t, 200, code, "%v", moved)
	assert.EqualValues(t, id, moved["goal_id"])
	code, page := call("GET", "/api/board/team/tasks?goal="+itoa(id), nil)
	require.Equal(t, 200, code)
	assert.Len(t, page["tasks"], 2)
	assert.EqualValues(t, 2, page["total_open"])
	assert.Equal(t, "", page["next_cursor"])
	code, g = call("GET", "/api/board/team/goals/"+itoa(id), nil)
	require.Equal(t, 200, code)
	assert.EqualValues(t, 2, g["rollup"].(map[string]any)["queued"])
	code, moved = call("PUT", "/api/board/team/tasks/"+itoa(task2.ID)+"/goal", map[string]any{"goal_id": nil, "subscriber_id": "Orchestrator"})
	require.Equal(t, 200, code, "%v", moved)
	assert.Nil(t, moved["goal_id"])

	// Closing warns how many tasks were still open and leaves them; a closed goal takes no new task.
	code, closed := call("POST", "/api/board/team/goals/"+itoa(id)+"/close", map[string]any{"reason": "shipped", "subscriber_id": "Orchestrator"})
	require.Equal(t, 200, code, "%v", closed)
	assert.EqualValues(t, 1, closed["open_tasks"])
	assert.Equal(t, "done", closed["goal"].(map[string]any)["state"])
	code, out = call("POST", "/api/board/team/goals/"+itoa(id)+"/close", map[string]any{"reason": "again", "subscriber_id": "Orchestrator"})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "goal_already_closed", out["code"])
	before, _ := h.bs.ListTasks(ctx, "team")
	code, out = call("POST", "/api/board/team/tasks", map[string]any{"title": "Late", "subscriber_id": "Orchestrator", "goal_id": id})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "goal_closed", out["code"])
	after, _ := h.bs.ListTasks(ctx, "team")
	assert.Len(t, after, len(before), "the refused task was not stored")
	code, out = call("GET", "/api/board/team/goals", nil)
	require.Equal(t, 200, code)
	assert.Empty(t, goalsOf(t, out), "a closed goal is not in the default list")
	code, out = call("GET", "/api/board/team/goals?state=done,dropped", nil)
	require.Equal(t, 200, code)
	assert.Len(t, goalsOf(t, out), 1)
}

const canonicalGoalUUID = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

// Reads are open to anyone who can read the board; every write is the Operator's or an orchestrator's, and a worker agent's refusal changes nothing.
func TestGoalWritesArePlannersOnly(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	registerTaskPlanner(t, h, "team", "Orchestrator")
	_, err := h.bs.Subscribe(ctx, "team", "Dev", "Developer", "tmux-dev", nil, "mentions", false)
	require.NoError(t, err)
	call := goalCall(t, server.URL)
	_, g := call("POST", "/api/board/team/goals", map[string]any{"title": "Planner's goal", "subscriber_id": "Orchestrator"})
	id := int64(g["id"].(float64))
	task, err := h.bs.CreateTask(ctx, "team", "A task", "", "medium", "Orchestrator")
	require.NoError(t, err)

	for name, req := range map[string]struct {
		method, path string
		body         map[string]any
	}{
		"create": {"POST", "/api/board/team/goals", map[string]any{"title": "Dev's goal"}},
		"update": {"PATCH", "/api/board/team/goals/" + itoa(id), map[string]any{"title": "Hijacked"}},
		"close":  {"POST", "/api/board/team/goals/" + itoa(id) + "/close", map[string]any{"reason": "no"}},
		"link":   {"PUT", "/api/board/team/tasks/" + itoa(task.ID) + "/goal", map[string]any{"goal_id": id}},
	} {
		req.body["subscriber_id"] = "Dev"
		code, out := call(req.method, req.path, req.body)
		assert.Equal(t, http.StatusForbidden, code, "%s: %v", name, out)
	}
	code, got := call("GET", "/api/board/team/goals/"+itoa(id), nil)
	require.Equal(t, 200, code)
	assert.Equal(t, "Planner's goal", got["title"])
	assert.Equal(t, "active", got["state"])
	code, out := call("GET", "/api/board/team/goals", nil)
	require.Equal(t, 200, code, "a read needs no identity")
	assert.Len(t, goalsOf(t, out), 1, "the worker's create made none")
	stored, _ := h.bs.GetTask(ctx, "team", task.ID)
	assert.Nil(t, stored.GoalID)
}

// A goal of another board, an unknown one and a closed-over typo all answer the same words, so nothing is learned about other boards.
func TestAGoalOfAnotherBoardLooksLikeNoGoal(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	registerTaskPlanner(t, h, "team", "Orchestrator")
	registerTaskPlanner(t, h, "beta", "Orchestrator")
	call := goalCall(t, server.URL)
	_, g := call("POST", "/api/board/beta/goals", map[string]any{"title": "Beta's goal", "subscriber_id": "Orchestrator"})
	foreign := int64(g["id"].(float64))
	task, err := h.bs.CreateTask(ctx, "team", "Team task", "", "medium", "Orchestrator")
	require.NoError(t, err)
	missing := foreign + 500
	answers := map[string]map[string]any{}
	for _, id := range []int64{foreign, missing} {
		for name, req := range map[string]struct {
			method, path string
			body         any
		}{
			"show":   {"GET", "/api/board/team/goals/" + itoa(id), nil},
			"update": {"PATCH", "/api/board/team/goals/" + itoa(id), map[string]any{"title": "x", "subscriber_id": "Orchestrator"}},
			"close":  {"POST", "/api/board/team/goals/" + itoa(id) + "/close", map[string]any{"reason": "x", "subscriber_id": "Orchestrator"}},
			"tasks":  {"GET", "/api/board/team/tasks?goal=" + itoa(id), nil},
			"link":   {"PUT", "/api/board/team/tasks/" + itoa(task.ID) + "/goal", map[string]any{"goal_id": id, "subscriber_id": "Orchestrator"}},
			"create": {"POST", "/api/board/team/tasks", map[string]any{"title": "Linked", "goal_id": id, "subscriber_id": "Orchestrator"}},
		} {
			code, out := call(req.method, req.path, req.body)
			assert.Equal(t, http.StatusNotFound, code, "%s %d: %v", name, id, out)
			assert.Equal(t, "goal_not_found", out["code"], name)
			if prev, ok := answers[name]; ok {
				assert.Equal(t, prev, out, "%s: another board's goal and an unknown one answer alike", name)
			}
			answers[name] = out
		}
	}
	tasks, _ := h.bs.ListTasks(ctx, "team")
	assert.Len(t, tasks, 1, "no task was stored by a refused create")
	got, _ := h.bs.GetTask(ctx, "team", task.ID)
	assert.Nil(t, got.GoalID)
	code, out := call("GET", "/api/board/beta/goals/"+itoa(foreign), nil)
	require.Equal(t, 200, code)
	assert.Equal(t, "Beta's goal", out["title"], "the goal is untouched on its own board")
}

func TestGoalRefusalsHaveFixedWords(t *testing.T) {
	server, h := setupBoardTestServer(t)
	registerTaskPlanner(t, h, "team", "Orchestrator")
	call := goalCall(t, server.URL)
	_, g := call("POST", "/api/board/team/goals", map[string]any{"title": "ok", "subscriber_id": "Orchestrator"})
	id := itoa(int64(g["id"].(float64)))
	for _, c := range []struct {
		name, method, path string
		body               any
		status             int
		code               string
	}{
		{"no title", "POST", "/api/board/team/goals", map[string]any{"title": " ", "subscriber_id": "Orchestrator"}, 400, "goal_title_required"},
		{"long title", "POST", "/api/board/team/goals", map[string]any{"title": strings.Repeat("t", 201), "subscriber_id": "Orchestrator"}, 400, "goal_text_too_long"},
		{"blocked at create", "POST", "/api/board/team/goals", map[string]any{"title": "x", "state": "blocked", "subscriber_id": "Orchestrator"}, 400, "goal_bad_state"},
		{"unknown driver", "POST", "/api/board/team/goals", map[string]any{"title": "x", "driver": "Nobody", "subscriber_id": "Orchestrator"}, 400, "goal_driver_unknown"},
		{"close needs a reason", "POST", "/api/board/team/goals/" + id + "/close", map[string]any{"reason": "", "subscriber_id": "Orchestrator"}, 400, "goal_reason_required"},
		{"close as blocked", "POST", "/api/board/team/goals/" + id + "/close", map[string]any{"reason": "x", "state": "blocked", "subscriber_id": "Orchestrator"}, 400, "goal_bad_state"},
		{"update to done", "PATCH", "/api/board/team/goals/" + id, map[string]any{"state": "done", "subscriber_id": "Orchestrator"}, 400, "goal_reason_required"},
		{"bad state filter", "GET", "/api/board/team/goals?state=nonsense", nil, 400, "goal_bad_state"},
		{"bad id", "GET", "/api/board/team/goals/abc", nil, 400, ""},
		{"zero id", "GET", "/api/board/team/goals/0", nil, 400, ""},
		{"bad limit", "GET", "/api/board/team/goals?limit=-1", nil, 400, ""},
		{"link needs goal_id", "PUT", "/api/board/team/tasks/1/goal", map[string]any{"subscriber_id": "Orchestrator"}, 400, ""},
		{"link bad goal_id", "PUT", "/api/board/team/tasks/1/goal", map[string]any{"goal_id": "x", "subscriber_id": "Orchestrator"}, 400, ""},
		{"link unknown task", "PUT", "/api/board/team/tasks/999/goal", map[string]any{"goal_id": nil, "subscriber_id": "Orchestrator"}, 404, "task_not_found"},
		{"no actor", "POST", "/api/board/team/goals", map[string]any{"title": "x"}, 400, ""},
	} {
		code, out := call(c.method, c.path, c.body)
		assert.Equal(t, c.status, code, "%s: %v", c.name, out)
		if c.code != "" {
			assert.Equal(t, c.code, out["code"], c.name)
		}
		assert.NotEmpty(t, out["error"], c.name)
	}
}

// The coral-hub condition: a goal write only writes. It calls no terminal step, sends nothing to any agent and starts, wakes or kills no session, whoever the driver is and whatever state
// the goal moves to.
func TestGoalRoutesStartNothing(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	registerTaskPlanner(t, h, "team", "Orchestrator")
	_, err := h.bs.Subscribe(ctx, "team", "Sleeper", "Developer", "tmux-sleeper", nil, "mentions", false)
	require.NoError(t, err)
	terminal := newMockTerminal() // no session "tmux-sleeper": that agent is asleep
	terminal.onCall = func(method string) { t.Errorf("a goal write reached the terminal: %s", method) }
	h.SetTerminal(terminal)
	call := goalCall(t, server.URL)
	code, g := call("POST", "/api/board/team/goals", map[string]any{"title": "Asleep driver", "driver": "Sleeper", "subscriber_id": "Orchestrator"})
	require.Equal(t, http.StatusCreated, code, "%v", g)
	id := itoa(int64(g["id"].(float64)))
	task, err := h.bs.CreateTask(ctx, "team", "A task", "", "medium", "Orchestrator")
	require.NoError(t, err)
	for _, step := range []struct {
		method, path string
		body         map[string]any
	}{
		{"PATCH", "/api/board/team/goals/" + id, map[string]any{"state": "blocked", "blocked_on": "Sleeper", "decision": "wake up?"}},
		{"PUT", "/api/board/team/tasks/" + itoa(task.ID) + "/goal", map[string]any{"goal_id": int64(g["id"].(float64))}},
		{"PATCH", "/api/board/team/goals/" + id, map[string]any{"state": "active", "driver": "Sleeper"}},
		{"POST", "/api/board/team/goals/" + id + "/close", map[string]any{"reason": "done"}},
	} {
		step.body["subscriber_id"] = "Orchestrator"
		code, out := call(step.method, step.path, step.body)
		require.Equal(t, 200, code, "%s %s: %v", step.method, step.path, out)
	}
	h.WaitBackground()
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	assert.Empty(t, terminal.sent, "nothing was typed into any terminal")
	assert.Empty(t, terminal.raw)
	assert.Empty(t, terminal.origins)
	assert.Empty(t, terminal.killSessionCalls)
	assert.Empty(t, terminal.sessions, "no session was created")
}

// A board made now reads its uid; one made before reads null.
func TestBoardInfoCarriesTheBoardUID(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	call := goalCall(t, server.URL)
	b, err := h.bs.CreateBoard(ctx, "Sprint", "Operator")
	require.NoError(t, err)
	code, out := call("GET", "/api/board/"+b.ID, nil)
	require.Equal(t, 200, code)
	assert.Equal(t, *b.UID, out["board_uid"])
	assert.Regexp(t, canonicalGoalUUID, out["board_uid"])
	_, err = h.bs.CreateTask(ctx, "oldname", "t", "", "medium", "Operator")
	require.NoError(t, err)
	code, out = call("GET", "/api/board/oldname", nil)
	require.Equal(t, 200, code)
	val, present := out["board_uid"]
	assert.True(t, present, "the field is there")
	assert.Nil(t, val, "an older board reads null")
}

// Through Coral Hub (the connector's stamp and a hub user): a goal write is made as the Operator by the Hub's user and recorded with them, is refused when the owner has turned "Don't let Coral Hub act as
// the Operator" on or when it claims an orchestrator's name, and a goal create or a task add with an Idempotency-Key runs once per hub user, even when it is sent again inside the key's life.
func TestGoalRoutesThroughCoralHub(t *testing.T) {
	bs, err := boardtest.Open(t.TempDir() + "/board.db")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	h := NewBoardHandler(bs)
	t.Cleanup(h.WaitBackground)
	ctx := context.Background()
	_, err = bs.Subscribe(ctx, "team", "Orchestrator", "Orchestrator", "tmux-orch", nil, "mentions")
	require.NoError(t, err)
	allowed := true
	r := chi.NewRouter()
	r.Use(TunnelIdentityGuard(bs, func(*http.Request) bool { return allowed }))
	r.Use(NewIdempotency().Middleware)
	r.Post("/api/board/{project}/goals", h.CreateGoal)
	r.Patch("/api/board/{project}/goals/{goalID}", h.UpdateGoal)
	r.Post("/api/board/{project}/goals/{goalID}/close", h.CloseGoal)
	r.Post("/api/board/{project}/tasks", h.CreateTask)
	r.Put("/api/board/{project}/tasks/{taskID}/goal", h.SetTaskGoal)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	viaHub := func(user, key, method, path string, body any) (int, map[string]any, http.Header) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
		req.Header.Set(tunnel.HubUserHeader, user)
		if key != "" {
			req.Header.Set(tunnel.IdempotencyKeyHeader, key)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out, resp.Header
	}
	const alice, bob = "hub-user-1111aaaa", "hub-user-2222bbbb"
	goals := func() []board.Goal {
		gs, err := bs.ListGoals(ctx, "team", board.GoalListQuery{})
		require.NoError(t, err)
		return gs
	}

	// The Operator through the Hub: made, attributed to the Operator, and recorded with the hub user.
	code, g, _ := viaHub(alice, "", "POST", "/api/board/team/goals", map[string]any{"title": "From the hub", "subscriber_id": "Operator"})
	require.Equal(t, http.StatusCreated, code, "%v", g)
	assert.Equal(t, "Operator", g["created_by"])
	acts, err := bs.RecentHubOperatorActions(ctx, 10)
	require.NoError(t, err)
	require.NotEmpty(t, acts)
	assert.Equal(t, alice, acts[0].HubUser)
	assert.Equal(t, "/api/board/team/goals", acts[0].Path)

	// Refused: an orchestrator's name, and the owner's opt-out.
	code, out, _ := viaHub(alice, "", "POST", "/api/board/team/goals", map[string]any{"title": "As the orchestrator", "subscriber_id": "Orchestrator"})
	assert.Equal(t, http.StatusForbidden, code, "%v", out)
	allowed = false
	code, out, _ = viaHub(alice, "", "POST", "/api/board/team/goals", map[string]any{"title": "Opted out", "subscriber_id": "Operator"})
	assert.Equal(t, http.StatusForbidden, code, "%v", out)
	assert.Equal(t, "operator_identity_required", out["code"])
	allowed = true
	assert.Len(t, goals(), 1, "the two refusals stored nothing")

	// Idempotency: the same key from the same hub user and body makes one goal and answers the first answer again; another hub user, or another body, is its own.
	const key = "preview-token-0123456789"
	body := map[string]any{"title": "Once", "subscriber_id": "Operator"}
	code, first, _ := viaHub(alice, key, "POST", "/api/board/team/goals", body)
	require.Equal(t, http.StatusCreated, code, "%v", first)
	code, again, hdr := viaHub(alice, key, "POST", "/api/board/team/goals", body)
	assert.Equal(t, http.StatusCreated, code)
	assert.Equal(t, first["id"], again["id"], "the replay is the first answer")
	assert.Equal(t, "true", hdr.Get("Idempotent-Replay"))
	assert.Len(t, goals(), 2, "one goal was made for two sends")
	code, other, _ := viaHub(bob, key, "POST", "/api/board/team/goals", body)
	require.Equal(t, http.StatusCreated, code, "%v", other)
	assert.NotEqual(t, first["id"], other["id"], "another hub user's key is its own")
	code, _, _ = viaHub(alice, key, "POST", "/api/board/team/goals", map[string]any{"title": "Another body", "subscriber_id": "Operator"})
	assert.Equal(t, http.StatusUnprocessableEntity, code, "the same key with another body is refused")
	assert.Len(t, goals(), 3)

	// A task add under the goal, sent twice with its key, is one task.
	gid := int64(first["id"].(float64))
	task := map[string]any{"title": "Task once", "subscriber_id": "Operator", "goal_id": gid}
	code, t1, _ := viaHub(alice, "task-key-0123456789ab", "POST", "/api/board/team/tasks", task)
	require.Equal(t, http.StatusCreated, code, "%v", t1)
	_, t2, hdr := viaHub(alice, "task-key-0123456789ab", "POST", "/api/board/team/tasks", task)
	assert.Equal(t, t1["id"], t2["id"])
	assert.Equal(t, "true", hdr.Get("Idempotent-Replay"))
	tasks, _, err := bs.GoalTasks(ctx, "team", gid)
	require.NoError(t, err)
	assert.Len(t, tasks, 1, "one task for two sends")

	// The other goal writes are the Hub's Operator too, through the same guard.
	code, out, _ = viaHub(alice, "", "PATCH", "/api/board/team/goals/"+itoa(gid), map[string]any{"title": "Renamed", "subscriber_id": "Operator"})
	assert.Equal(t, 200, code, "%v", out)
	code, out, _ = viaHub(alice, "", "POST", "/api/board/team/goals/"+itoa(gid)+"/close", map[string]any{"reason": "done", "subscriber_id": "Operator"})
	assert.Equal(t, 200, code, "%v", out)
}

// A goal's task read says when it is cut (Frontend's #7135 finding): past board.MaxGoalTasks the page says truncated; under it, no flag.
func TestGoalTasksReadSaysWhenItIsTruncated(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	registerTaskPlanner(t, h, "team", "Orchestrator")
	call := goalCall(t, server.URL)
	_, g := call("POST", "/api/board/team/goals", map[string]any{"title": "Many tasks", "subscriber_id": "Orchestrator"})
	gid := int64(g["id"].(float64))
	for i := 0; i < board.MaxGoalTasks; i++ {
		_, err := h.bs.CreateTaskWithOpts(ctx, "team", "t", "", "medium", "Orchestrator", &board.CreateTaskOpts{GoalID: &gid})
		require.NoError(t, err)
	}
	code, page := call("GET", "/api/board/team/tasks?goal="+itoa(gid), nil)
	require.Equal(t, 200, code)
	assert.Len(t, page["tasks"], board.MaxGoalTasks)
	_, has := page["truncated"]
	assert.False(t, has, "exactly the cap is not a truncation")
	_, err := h.bs.CreateTaskWithOpts(ctx, "team", "one too many", "", "medium", "Orchestrator", &board.CreateTaskOpts{GoalID: &gid})
	require.NoError(t, err)
	code, page = call("GET", "/api/board/team/tasks?goal="+itoa(gid), nil)
	require.Equal(t, 200, code)
	assert.Len(t, page["tasks"], board.MaxGoalTasks)
	assert.Equal(t, true, page["truncated"])
	assert.EqualValues(t, board.MaxGoalTasks, page["total_open"])
}
