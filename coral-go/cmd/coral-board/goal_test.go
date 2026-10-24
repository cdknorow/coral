package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #7116: the goal commands and the task link, against a server that answers like the goal routes (#7087): the requests they send, what they print, and what they refuse.

type goalServer struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []string // "METHOD path?query body"
	status int      // the status of the next answers (0: the routes' own)
	reply  map[string]string
}

func newGoalServer(t *testing.T) *goalServer {
	gs := &goalServer{reply: map[string]string{}}
	gs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		gs.mu.Lock()
		gs.reqs = append(gs.reqs, key+"?"+r.URL.RawQuery+" "+string(body))
		status := gs.status
		canned, ok := gs.reply[key]
		gs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"that goal is not on this board","code":"goal_not_found"}`))
			return
		}
		if ok {
			_, _ = w.Write([]byte(canned))
			return
		}
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/goals"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":7,"title":"x","state":"active"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(gs.Close)
	return gs
}

func (gs *goalServer) requests() []string {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	return append([]string(nil), gs.reqs...)
}

func goalRun(t *testing.T, gs *goalServer, args ...string) boardRun {
	t.Helper()
	return runBoard(t, gs.URL, subscribedDataDir(t, gs.URL), args...)
}

func bodyOf(t *testing.T, req string) map[string]any {
	t.Helper()
	i := strings.Index(req, " {")
	require.GreaterOrEqual(t, i, 0, req)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(req[i+1:]), &m), req)
	return m
}

func TestGoalAddSendsTheFieldsAndPrintsTheId(t *testing.T) {
	gs := newGoalServer(t)
	res := goalRun(t, gs, "goal", "add", "Ship teams", "--outcome", "Orgs own machines", "--done-when", "an admin approves one", "--state", "proposed", "--driver", "Orchestrator")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "Created Goal #7: Ship teams\n", res.stdout)
	reqs := gs.requests()
	require.Len(t, reqs, 1)
	assert.True(t, strings.HasPrefix(reqs[0], "POST /api/board/team/goals? "), reqs[0])
	b := bodyOf(t, reqs[0])
	assert.Equal(t, map[string]any{"title": "Ship teams", "outcome": "Orgs own machines", "done_when": "an admin approves one", "state": "proposed", "driver": "Orchestrator", "subscriber_id": "Tester"}, b)

	// Options may stand before the title, and only the title is required.
	res = goalRun(t, gs, "goal", "add", "--driver", "QA", "Plain")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, map[string]any{"title": "Plain", "driver": "QA", "subscriber_id": "Tester"}, bodyOf(t, gs.requests()[1]))

	res = goalRun(t, gs, "goal", "add")
	assert.Equal(t, 1, res.code)
	assert.Contains(t, res.stderr, "Usage: coral-board goal add")
	assert.Len(t, gs.requests(), 2, "no request without a title")
}

func TestGoalListAndShowRead(t *testing.T) {
	gs := newGoalServer(t)
	gs.reply["GET /api/board/team/goals"] = `{"goals":[{"id":3,"title":"Ship teams","state":"active","driver":"Orchestrator","rollup":{"done":2,"active":1,"blocked":0,"queued":2,"total":5}},{"id":4,"title":"No tasks","state":"blocked","rollup":{"total":0}}]}`
	res := goalRun(t, gs, "goal", "list")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "#3   active    Orchestrator    2/5       Ship teams")
	assert.Contains(t, res.stdout, "#4   blocked   —               —         No tasks")
	assert.Equal(t, "GET /api/board/team/goals? ", gs.requests()[0][:len("GET /api/board/team/goals? ")], "open goals: no state given")

	res = goalRun(t, gs, "goal", "list", "--state", "all", "--json")
	require.Equal(t, 0, res.code, res.stderr)
	var arr []map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &arr), res.stdout)
	assert.Len(t, arr, 2)
	assert.Contains(t, gs.requests()[1], "state=proposed,active,blocked,done,dropped")

	gs.reply["GET /api/board/team/goals/3"] = `{"id":3,"title":"Ship teams","state":"blocked","driver":"Orchestrator","blocked_on":"the operator","decision":"pick a date","recommendation":"Friday","if_silent":"we ship Friday","rollup":{"done":1,"active":0,"blocked":1,"queued":0,"total":2}}`
	gs.reply["GET /api/board/team/tasks"] = `{"tasks":[{"id":11,"title":"Build it","status":"completed","assigned_to":"Backend Dev"},{"id":12,"title":"Test it","status":"blocked"}],"next_cursor":""}`
	res = goalRun(t, gs, "goal", "show", "3")
	require.Equal(t, 0, res.code, res.stderr)
	for _, want := range []string{"Goal #3: Ship teams", "State:", "blocked", "Blocked on:", "the operator", "Decision:", "pick a date", "Recommendation:", "Friday", "If silent:", "we ship Friday", "1 done, 0 active, 1 blocked, 0 queued (2)", "#11   completed    Backend Dev", "#12   blocked      —"} {
		assert.Contains(t, res.stdout, want)
	}
	assert.Contains(t, strings.Join(gs.requests(), "\n"), "goal=3", "the goal's own tasks")

	res = goalRun(t, gs, "goal", "show", "3", "--json")
	require.Equal(t, 0, res.code, res.stderr)
	var one map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &one), res.stdout)
	assert.Len(t, one["tasks"], 2)
}

func TestGoalUpdateSendsOnlyWhatWasGivenAndClears(t *testing.T) {
	gs := newGoalServer(t)
	res := goalRun(t, gs, "goal", "update", "3", "--state", "blocked", "--blocked-on", "the operator", "--decision", "pick a date", "--recommendation", "Friday", "--if-silent", "ship Friday")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "Updated Goal #3\n", res.stdout)
	req := gs.requests()[0]
	assert.True(t, strings.HasPrefix(req, "PATCH /api/board/team/goals/3? "), req)
	assert.Equal(t, map[string]any{"state": "blocked", "blocked_on": "the operator", "decision": "pick a date", "recommendation": "Friday", "if_silent": "ship Friday", "subscriber_id": "Tester"}, bodyOf(t, req))

	res = goalRun(t, gs, "goal", "update", "3", "--driver", "")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, map[string]any{"driver": "", "subscriber_id": "Tester"}, bodyOf(t, gs.requests()[1]), "an empty driver is sent: it frees the goal")

	res = goalRun(t, gs, "goal", "update", "3")
	assert.Equal(t, 1, res.code)
	assert.Contains(t, res.stderr, "Nothing to update")
	assert.Len(t, gs.requests(), 2)
}

func TestGoalCloseReportsOpenTasksAndNeedsAReason(t *testing.T) {
	gs := newGoalServer(t)
	gs.reply["POST /api/board/team/goals/3/close"] = `{"goal":{"id":3,"state":"dropped"},"open_tasks":2}`
	res := goalRun(t, gs, "goal", "close", "3", "--reason", "not doing it", "--state", "dropped")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "Closed Goal #3 (dropped)")
	assert.Contains(t, res.stdout, "2 tasks of this goal are still open")
	assert.Equal(t, map[string]any{"reason": "not doing it", "state": "dropped", "subscriber_id": "Tester"}, bodyOf(t, gs.requests()[0]))

	gs.reply["POST /api/board/team/goals/4/close"] = `{"goal":{"id":4,"state":"done"},"open_tasks":1}`
	res = goalRun(t, gs, "goal", "close", "4", "--reason", "shipped")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "1 task of this goal is still open")

	res = goalRun(t, gs, "goal", "close", "3")
	assert.Equal(t, 1, res.code)
	assert.Contains(t, res.stderr, "Usage: coral-board goal close")
	assert.Len(t, gs.requests(), 2, "no request without a reason")
}

func TestTaskGoalLinksMovesAndClears(t *testing.T) {
	gs := newGoalServer(t)
	res := goalRun(t, gs, "task", "goal", "11", "3")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "Task #11 is now on Goal #3\n", res.stdout)
	assert.True(t, strings.HasPrefix(gs.requests()[0], "PUT /api/board/team/tasks/11/goal? "), gs.requests()[0])
	assert.Equal(t, map[string]any{"goal_id": float64(3), "subscriber_id": "Tester"}, bodyOf(t, gs.requests()[0]))

	res = goalRun(t, gs, "task", "goal", "11", "none")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "Task #11 is no longer linked to a goal\n", res.stdout)
	assert.Equal(t, map[string]any{"goal_id": nil, "subscriber_id": "Tester"}, bodyOf(t, gs.requests()[1]), "null clears")

	for _, args := range [][]string{{"task", "goal", "11"}, {"task", "goal", "x", "3"}, {"task", "goal", "11", "zero"}, {"task", "goal", "11", "-2"}} {
		res = goalRun(t, gs, args...)
		assert.NotEqual(t, 0, res.code, args)
	}
	assert.Len(t, gs.requests(), 2, "a bad id sends nothing")
}

func TestTaskAddAndListTakeTheGoal(t *testing.T) {
	gs := newGoalServer(t)
	gs.reply["POST /api/board/team/tasks"] = `{"id":21}`
	res := goalRun(t, gs, "task", "add", "Do it", "--goal", "3")
	require.Equal(t, 0, res.code, res.stderr)
	b := bodyOf(t, gs.requests()[0])
	assert.Equal(t, float64(3), b["goal_id"])
	res = goalRun(t, gs, "task", "add", "No goal")
	require.Equal(t, 0, res.code, res.stderr)
	assert.NotContains(t, bodyOf(t, gs.requests()[1]), "goal_id", "no goal given: none sent")

	gs.reply["GET /api/board/team/tasks"] = `{"tasks":[{"id":11,"title":"Build it","status":"pending","priority":"high"}],"next_cursor":""}`
	res = goalRun(t, gs, "task", "list", "--goal", "3")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "Build it")
	assert.Contains(t, gs.requests()[2], "goal=3")
	res = goalRun(t, gs, "task", "list")
	assert.NotContains(t, gs.requests()[3], "goal=")

	for _, args := range [][]string{{"task", "list", "--goal", "abc"}, {"task", "list", "--goal", "0"}, {"task", "add", "x", "--goal", "-1"}} {
		res = goalRun(t, gs, args...)
		assert.NotEqual(t, 0, res.code, args)
	}
}

func TestGoalErrorsAreTheServersSentence(t *testing.T) {
	gs := newGoalServer(t)
	gs.status = 404
	res := goalRun(t, gs, "goal", "show", "9")
	assert.Equal(t, 6, res.code, res.stderr)
	assert.Contains(t, res.stderr, "that goal is not on this board")
	assert.Contains(t, res.stderr, "goal_not_found")
}

func TestGoalFlagsAreChecked(t *testing.T) {
	gs := newGoalServer(t)
	for _, args := range [][]string{
		{"goal", "list", "--jsno"}, {"goal", "show", "1", "2"}, {"goal", "add", "t", "--outcom", "x"}, {"goal", "close", "1", "--reasn", "x"}, {"goal", "update", "1", "--nope", "x"}, {"goal", "bogus"},
	} {
		res := goalRun(t, gs, args...)
		assert.Equal(t, 2, res.code, "%v: %s", args, res.stderr)
	}
	assert.Empty(t, gs.requests(), "nothing reaches the server")

	// A value that is an option by itself is refused rather than taken as the text, as for --body.
	res := goalRun(t, gs, "goal", "add", "t", "--outcome", "--fix")
	assert.Equal(t, 2, res.code, "an option as a value is refused with the usage")
	assert.Empty(t, gs.requests())
}

func TestGoalHelp(t *testing.T) {
	gs := newGoalServer(t)
	for _, args := range [][]string{{"goal", "--help"}, {"goal", "-h"}, {"goal", "help"}, {"goal", "add", "--help"}, {"help", "goal"}} {
		res := goalRun(t, gs, args...)
		assert.Equal(t, 0, res.code, args)
		assert.Contains(t, res.stdout, "coral-board goal", args)
	}
	assert.Empty(t, gs.requests())
	res := goalRun(t, gs, "--help")
	assert.Contains(t, res.stdout, "goal add/list/show/update/close")
	assert.Contains(t, res.stdout, "task goal <task> <goal|none>")
}

// #7087 round 2: a goal's task answer that is cut says so, and goal show says so too (the first 500 tasks are all the route sends).
func TestGoalShowSaysWhenItsTasksAreCut(t *testing.T) {
	gs := newGoalServer(t)
	gs.reply["GET /api/board/team/goals/3"] = `{"id":3,"title":"Big","state":"active","rollup":{"done":0,"active":0,"blocked":0,"queued":2,"total":600}}`
	gs.reply["GET /api/board/team/tasks"] = `{"tasks":[{"id":11,"title":"A","status":"pending"},{"id":12,"title":"B","status":"pending"}],"next_cursor":"","truncated":true}`
	res := goalRun(t, gs, "goal", "show", "3")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "only the first 2 tasks are listed")
	res = goalRun(t, gs, "goal", "show", "3", "--json")
	require.Equal(t, 0, res.code, res.stderr)
	var one map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &one))
	assert.Equal(t, true, one["tasks_truncated"])

	gs.reply["GET /api/board/team/tasks"] = `{"tasks":[{"id":11,"title":"A","status":"pending"}],"next_cursor":""}`
	res = goalRun(t, gs, "goal", "show", "3")
	assert.NotContains(t, res.stdout, "only the first")
}
