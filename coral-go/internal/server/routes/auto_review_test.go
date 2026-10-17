package routes

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
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

	err = h.bs.SetBoardSetting(ctx, "agentmade", board.DefaultReviewersKey, "worker, Security Reviewer")
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
	err := h.bs.SetBoardSetting(ctx, "off", AutoDelegateKey, "off")
	require.NoError(t, err)
	task, err := h.bs.CreateTask(ctx, "off", "work", "Reviewers: QA Engineer", "high", "lead", "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "off", "worker", task.ID)
	require.NoError(t, err)
	submit(t, base, task.ID)
	assert.Empty(t, reviewersOf(t, h, "off", task.ID))
}
