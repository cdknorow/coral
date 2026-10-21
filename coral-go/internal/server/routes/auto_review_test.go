package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
)

func reviewersOf(t *testing.T, h *BoardHandler, project string, id int64) []string {
	t.Helper()
	task, err := h.bs.GetTask(context.Background(), project, id)
	require.NoError(t, err)
	var out []string
	for _, r := range task.Workflow.ReviewRounds {
		for _, rv := range r.Reviewers {
			out = append(out, rv.Reviewer)
		}
	}
	return out
}

// #4175 step 1: a task the orchestrator made, naming its reviewers in its body, is delegated
// by Coral as it enters review: never to its author (#4176 rule 12), only to board members,
// each once, with a fixed board line that carries no title.
func TestAutoDelegationToTheNamedReviewers(t *testing.T) {
	base, h, first := reviewTeam(t, "auto", true)
	ctx := context.Background()
	submit(t, base, first.ID) // frees the worker's slot
	body := "Do it.\nReviewers: Security Reviewer, QA Engineer, worker, qa engineer, Nobody Here"
	task, err := h.bs.CreateTask(ctx, "auto", "Secret plan title", body, "high", "lead", "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "auto", "worker", task.ID)
	require.NoError(t, err)
	submit(t, base, task.ID)

	assert.Equal(t, []string{"Security Reviewer", "QA Engineer"}, reviewersOf(t, h, "auto", task.ID))
	msgs, err := h.bs.ListMessages(ctx, "auto", 200, 0, 0)
	require.NoError(t, err)
	var lines []string
	for _, m := range msgs {
		lines = append(lines, m.Content)
	}
	all := strings.Join(lines, "\n")
	assert.Contains(t, all, "delegated for review by Coral] to Security Reviewer (#")
	assert.Contains(t, all, board.ReviewersNamedInTask)
	assert.NotContains(t, all, "[Task #"+strconv.FormatInt(task.ID, 10)+" in review]", "the orchestrator has nothing to delegate")
	for _, l := range lines {
		if strings.Contains(l, "delegated for review by Coral") || strings.Contains(l, "assigned to you: the review of") {
			assert.NotContains(t, l, "Secret plan title", "no title in Coral's lines (#4176 rule 1)")
		}
	}
}

// An agent can't pick its own reviewers (#4176 rule 13): the names of a task it made, or one an
// agent amended, don't count. Then the team's default reviewers do (never its author), and with
// none the orchestrator delegates as before.
func TestAutoDelegationIgnoresNamesAnAgentGave(t *testing.T) {
	base, h, first := reviewTeam(t, "agentmade", true)
	ctx := context.Background()
	submit(t, base, first.ID) // frees the worker's slot
	mk := func(creator, body string, amendBy ...string) *board.Task {
		t.Helper()
		opts := &board.CreateTaskOpts{Workflow: board.TaskWorkflow{Reviewers: []string{"QA Engineer"}}}
		task, err := h.bs.CreateTaskWithOpts(ctx, "agentmade", "work", body, "high", creator, opts, "worker")
		require.NoError(t, err)
		for _, a := range amendBy { // before the claim, so the worker works on the amended revision
			_, err = h.bs.AmendTask(ctx, "agentmade", task.ID, a, task.Revision, "add reviewers", map[string]interface{}{"body": "Reviewers: Security Reviewer"})
			require.NoError(t, err)
		}
		_, err = h.bs.ClaimTask(ctx, "agentmade", "worker", task.ID)
		require.NoError(t, err)
		return task
	}
	own := mk("worker", "Reviewers: QA Engineer")
	submit(t, base, own.ID)
	assert.Empty(t, reviewersOf(t, h, "agentmade", own.ID), "named by the agent itself: the orchestrator decides")
	got, err := h.bs.GetTask(ctx, "agentmade", own.ID)
	require.NoError(t, err)
	assert.Equal(t, "review_pending", got.Status)
	assert.Equal(t, "lead", *got.AssignedTo)

	amended := mk("lead", "plain", "worker")
	code, out := postCode(t, base(amended.ID, "submit-review"), map[string]any{"subscriber_id": "worker", "reason": "ready", "message": "built it", "expected_revision": 2,
		"artifacts": []board.TaskArtifact{{Name: "report", Content: "done"}}})
	require.Equal(t, 200, code, "%v", out)
	assert.Empty(t, reviewersOf(t, h, "agentmade", amended.ID), "an agent's amendment: the names don't count")

	hs := board.DefaultHealthSettings()
	hs.DefaultReviewers = []string{"worker", "Security Reviewer"}
	_, err = h.bs.UpdateBoardSettings(ctx, "agentmade", board.BoardSettingsUpdate{Health: &hs})
	require.NoError(t, err)
	third := mk("worker", "Reviewers: worker")
	submit(t, base, third.ID)
	assert.Equal(t, []string{"Security Reviewer"}, reviewersOf(t, h, "agentmade", third.ID), "the team's defaults, without the author")

	byLead := mk("lead", "plain") // --reviewers "QA Engineer", given by the orchestrator
	submit(t, base, byLead.ID)
	assert.Equal(t, []string{"QA Engineer"}, reviewersOf(t, h, "agentmade", byLead.ID), "the reviewers field wins over the defaults")
}

// The setting turns it off.
func TestAutoDelegationCanBeTurnedOff(t *testing.T) {
	base, h, first := reviewTeam(t, "off", true)
	ctx := context.Background()
	submit(t, base, first.ID) // frees the worker's slot
	hs := board.DefaultHealthSettings()
	hs.AutoDelegate.On = false
	_, err := h.bs.UpdateBoardSettings(ctx, "off", board.BoardSettingsUpdate{Health: &hs})
	require.NoError(t, err)
	task, err := h.bs.CreateTask(ctx, "off", "work", "Reviewers: QA Engineer", "high", "lead", "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "off", "worker", task.ID)
	require.NoError(t, err)
	submit(t, base, task.ID)
	assert.Empty(t, reviewersOf(t, h, "off", task.ID))
}

// #4962, the operator: handing reviews to the default reviewers is its own setting, independent of the health check. The check is off for
// a new team, and the delegation still works; turned off by its own switch it stops, whether the check is on or off.
func TestAutoDelegationIsIndependentOfTheHealthCheck(t *testing.T) {
	for _, tc := range []struct {
		name          string
		checkOn       bool
		autoDelegate  bool
		wantDelegated bool
	}{
		{"check off, delegation on (the default)", false, true, true},
		{"check on, delegation on", true, true, true},
		{"check on, delegation off", true, false, false},
		{"check off, delegation off", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, h, first := reviewTeam(t, "indep", true)
			ctx := context.Background()
			submit(t, base, first.ID)
			hs := board.DefaultHealthSettings()
			hs.On = tc.checkOn
			hs.AutoDelegate.On = tc.autoDelegate
			_, err := h.bs.UpdateBoardSettings(ctx, "indep", board.BoardSettingsUpdate{Health: &hs})
			require.NoError(t, err)
			task, err := h.bs.CreateTask(ctx, "indep", "work", "Reviewers: QA Engineer", "high", "lead", "worker")
			require.NoError(t, err)
			_, err = h.bs.ClaimTask(ctx, "indep", "worker", task.ID)
			require.NoError(t, err)
			submit(t, base, task.ID)
			if tc.wantDelegated {
				assert.Equal(t, []string{"QA Engineer"}, reviewersOf(t, h, "indep", task.ID))
			} else {
				assert.Empty(t, reviewersOf(t, h, "indep", task.ID))
			}
		})
	}
}

// #4175, Security's caveat on #4176 rule 13: how the creator's and each amendment's names were
// backed is recorded by the server, whatever the request says, from the request's actor (#4255):
// "token" for a member of the board, "operator" for the Operator (the dashboard, a signed command
// line, an API key), "claimed" for a bare name. The handler is mounted behind a stand-in for the
// identity layer that sets the actor the layer would have decided.
func TestTaskRecordsHowItsNamesWereBacked(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "ids", "lead", "Orchestrator", "lead-session", nil, "all")
	require.NoError(t, err)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("X-Test-Actor") {
			case "token":
				r = withActor(r, Actor{Kind: ActorToken, Name: "lead", Session: "lead-session"})
			case "operator":
				r = withActor(r, Actor{Kind: ActorOperator, Name: "Operator"})
			default:
				r = withActor(r, Actor{Kind: ActorClaimed})
			}
			next.ServeHTTP(w, r)
		})
	})
	router.Post("/api/board/{project}/tasks", h.CreateTask)
	create := func(actor string, body map[string]any) *board.Task {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/board/ids/tasks", bytes.NewReader(raw))
		req.Header.Set("X-Test-Actor", actor)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, 201, rec.Code, rec.Body.String())
		var out struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		task, err := h.bs.GetTask(ctx, "ids", out.ID)
		require.NoError(t, err)
		return task
	}
	// A bare name, with forged "recorded" values in the body: claimed, and the forged values are replaced.
	task := create("claimed", map[string]any{"title": "work", "subscriber_id": "lead",
		"workflow": map[string]any{"created_identity": "token", "amend_identity": map[string]string{"2": "token"}}})
	id := task.ID
	assert.Equal(t, "claimed", task.Workflow.CreatedIdentity, "a forged value is replaced")
	assert.Empty(t, task.Workflow.AmendIdentity)

	// A member of the board, by its token: "token". (CreateTask still needs the claim; the task verbs
	// read the actor in the next commit.)
	assert.Equal(t, "token", create("token", map[string]any{"title": "work", "subscriber_id": "lead"}).Workflow.CreatedIdentity)

	// The Operator, with the dashboard's name. (Another spelling of it is claimIdentity's table; the
	// task planner check still compares the claim to "Operator" exactly: the next commit moves it to the actor.)
	assert.Equal(t, "operator", create("operator", map[string]any{"title": "work", "subscriber_id": "Operator"}).Workflow.CreatedIdentity)
	// ...and the same name with nothing behind it is a claim.
	assert.Equal(t, "claimed", create("claimed", map[string]any{"title": "work", "subscriber_id": "Operator"}).Workflow.CreatedIdentity)

	require.NoError(t, h.bs.RecordAmendIdentity(ctx, "ids", id, 2, "operator"))
	task, err = h.bs.GetTask(ctx, "ids", id)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"2": "operator"}, task.Workflow.AmendIdentity)
}
