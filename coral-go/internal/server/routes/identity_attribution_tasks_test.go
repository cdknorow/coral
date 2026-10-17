package routes

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The task verbs read the actor (#4255 commit 3): create, claim, current, complete, cancel, update,
// amend, reassign, publish, submit-review, release-review, delegate, approve, reject, nudge, snooze and
// reminders. Under enforce a request with no verified actor (no credential) is refused 403
// identity_unverified, a member's token or the Operator's cookie is attributed with no claim at all
// (the claim is optional), and in log mode nothing changes: the claim is required as before and a
// claimed caller is counted.
func TestTaskVerbsReadTheActor(t *testing.T) {
	type verb struct {
		name, method, path, body string
		needsClaim               bool // log mode answers 400 "...required" when the request names no one
	}
	verbs := func(id int64) []verb {
		t := fmt.Sprintf("/api/board/team/tasks/%d", id)
		return []verb{
			{"create", "POST", "/api/board/team/tasks", `{"title":"new work"}`, true},
			{"claim", "POST", "/api/board/team/tasks/claim", `{}`, true},
			{"current", "POST", "/api/board/team/tasks/current", `{}`, true},
			{"complete", "POST", t + "/complete", `{"message":"done"}`, true},
			{"cancel", "POST", t + "/cancel", `{"message":"no"}`, true},
			{"submit-review", "POST", t + "/submit-review", `{"reason":"review pending"}`, true},
			{"delegate", "POST", t + "/review", `{"reviewers":["Dev"]}`, true},
			{"approve", "POST", t + "/approve", `{"round":1}`, true},
			{"reject", "POST", t + "/reject", `{"round":1,"note":"no"}`, true},
			{"publish", "POST", t + "/publish", `{}`, true},
			{"update", "PATCH", t, `{"title":"renamed"}`, false},
			{"amend", "PATCH", t + "/amend", `{"base_revision":1,"reason":"r","changes":{"body":"b"}}`, false},
			{"reassign", "POST", t + "/reassign", `{"assigned_to":"Dev"}`, false},
			{"release-review", "POST", t + "/release-review", `{"reason":"slot released"}`, false},
			{"nudge", "POST", t + "/nudge", `{}`, false},
			{"snooze", "POST", t + "/snooze", `{"seconds":600}`, false},
			{"start reminders", "POST", t + "/reminder", `{"interval_seconds":600}`, false},
			{"stop reminders", "DELETE", t + "/reminder", ``, false},
		}
	}
	unverified := func(code int, body string) bool {
		return code == http.StatusForbidden && strings.Contains(body, "identity_unverified")
	}
	required := func(code int, body string) bool {
		return code == http.StatusBadRequest && strings.Contains(body, "required")
	}

	a := newAttributionHarness(t)
	for _, v := range verbs(a.taskID) {
		t.Run("log/"+v.name, func(t *testing.T) {
			setMode(t, false)
			for _, actor := range []string{"orch", "dev", "operator", "claimed"} {
				code, body := a.callBody(actor, v.method, v.path, v.body)
				assert.False(t, unverified(code, body), "log mode never refuses for an unverified actor: %s as %s -> %d %s", v.name, actor, code, body)
			}
			if v.needsClaim { // with no claim at all it is still the 400 it always was
				code, body := a.callBody("dev", v.method, v.path, v.body)
				assert.True(t, required(code, body), "%s with a token and no claim in log mode -> %d %s", v.name, code, body)
			}
			code, _ := a.callBody("claimed", v.method, v.path, v.body)
			_ = code
			assert.Contains(t, enforceDiffersSites(), "attribution "+v.method+" "+routeOfPath(v.path), "a claimed caller is counted: %s", v.name)
		})
		t.Run("enforce/"+v.name, func(t *testing.T) {
			setMode(t, true)
			code, body := a.callBody("claimed", v.method, v.path, v.body)
			assert.True(t, unverified(code, body), "no credential is refused: %s -> %d %s", v.name, code, body)
			for _, actor := range []string{"dev", "operator"} {
				code, body := a.callBody(actor, v.method, v.path, v.body)
				assert.False(t, unverified(code, body), "%s as %s is attributed, not refused as unverified: %d %s", v.name, actor, code, body)
				assert.False(t, required(code, body), "%s as %s needs no claim: %d %s", v.name, actor, code, body)
			}
		})
	}
}

// routeOfPath is the chi route pattern the task verbs' paths match in the harness.
func routeOfPath(path string) string {
	parts := strings.Split(path, "/") // "", api, board, <project>, tasks, <id>, <verb>
	parts[3] = "{project}"
	if len(parts) > 5 && parts[5] != "" && parts[5][0] >= '0' && parts[5][0] <= '9' {
		parts[5] = "{taskID}"
	}
	return strings.Join(parts, "/")
}

// The work is attributed to the token's member when the request names no one (enforce), the
// planner check follows the actor, and the Operator's spelling of its own name is the Operator's.
func TestTaskVerbsAttributeToTheActor(t *testing.T) {
	a := newAttributionHarness(t)
	ctx := context.Background()
	setMode(t, true)

	// Dev claims its task with no claim at all: it is Dev's active task.
	code, body := a.callBody("dev", "POST", "/api/board/team/tasks/claim", fmt.Sprintf(`{"task_id":%d}`, a.taskID))
	require.Equal(t, http.StatusOK, code, body)
	require.NotNil(t, a.bh.bs.ActiveTaskForSubscriber(ctx, "team", "Dev"), "the task is Dev's")

	// The orchestrator creates a task with no claim: its creator is the orchestrator, recorded as a token.
	code, body = a.callBody("orch", "POST", "/api/board/team/tasks", `{"title":"by the orchestrator"}`)
	require.Equal(t, http.StatusCreated, code, body)
	// A member that is not the orchestrator is not a planner, whatever name it gives.
	code, _ = a.callBody("dev", "POST", "/api/board/team/tasks", `{"title":"by a member"}`)
	assert.Equal(t, http.StatusForbidden, code)
	// The Operator's cookie plans tasks, with no claim and with the lower-case spelling the signed command line sends.
	for _, claim := range []string{``, `,"subscriber_id":"Operator"`, `,"subscriber_id":"operator"`} {
		code, body = a.callBody("operator", "POST", "/api/board/team/tasks", `{"title":"by the Operator"`+claim+`}`)
		assert.Equal(t, http.StatusCreated, code, "Operator with claim %q: %s", claim, body)
	}
	tasks, err := a.bh.bs.ListOpenTasks(ctx, "team")
	require.NoError(t, err)
	creators := map[string]string{}
	for _, listed := range tasks {
		task, err := a.bh.bs.GetTask(ctx, "team", listed.ID) // the list leaves the workflow out
		require.NoError(t, err)
		creators[task.Title] = task.CreatedBy + "/" + task.Workflow.CreatedIdentity
	}
	assert.Equal(t, "Orchestrator/token", creators["by the orchestrator"])
	assert.Equal(t, "Operator/operator", creators["by the Operator"])

	// In log mode a request that claims to be the Operator with nothing behind it still passes the planner
	// check (as before), and is counted; the lower-case spelling from a VERIFIED Operator now passes too.
	setMode(t, false)
	code, _ = a.callBody("claimed", "POST", "/api/board/team/tasks", `{"title":"claimed operator","subscriber_id":"Operator"}`)
	assert.Equal(t, http.StatusCreated, code)
	assert.Contains(t, enforceDiffersSites(), "task-planner POST /api/board/{project}/tasks")
	code, body = a.callBody("operator", "POST", "/api/board/team/tasks", `{"title":"lower case","subscriber_id":"operator"}`)
	assert.Equal(t, http.StatusCreated, code, body)
}

// requireTaskPlanner is the second lock behind attributedTo: called directly, as another handler
// might, it decides by the actor too. A verified Operator plans in any spelling; a claimant of the
// exact name "Operator" with nothing behind it passes in log mode (counted) and is refused under enforce.
func TestPlannerCheckDecidesByTheActor(t *testing.T) {
	a := newAttributionHarness(t)
	check := func(actor *Actor, name string) bool {
		r := httptest.NewRequest("POST", "/api/board/team/tasks", nil)
		if actor != nil {
			r = withActor(r, *actor)
		}
		return a.bh.requireTaskPlanner(httptest.NewRecorder(), r, "team", name)
	}
	operator := &Actor{Kind: ActorOperator, Name: "Operator"}
	claimed := &Actor{Kind: ActorClaimed}
	orch := &Actor{Kind: ActorToken, Name: "Orchestrator", Session: "claude-orch"}
	dev := &Actor{Kind: ActorToken, Name: "Dev", Session: "claude-dev"}

	setMode(t, false)
	assert.True(t, check(operator, "operator"), "the verified Operator, in the signed command line's spelling")
	assert.True(t, check(claimed, "Operator"), "log mode: a claimant of the exact name passes, as before")
	assert.Contains(t, enforceDiffersSites(), "task-planner POST (unrouted)", "and is counted")
	assert.True(t, check(orch, "Orchestrator"))
	assert.False(t, check(dev, "Dev"))

	setMode(t, true)
	assert.True(t, check(operator, "operator"))
	assert.False(t, check(claimed, "Operator"), "enforce: nothing behind the name is refused")
	assert.False(t, check(nil, "Operator"), "and so is a request that did not pass the identity layer")
	assert.True(t, check(orch, "Orchestrator"))
	assert.False(t, check(dev, "Dev"))
}
