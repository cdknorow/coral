package board

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func patchRow(name, digest string) TaskArtifact {
	return TaskArtifact{Name: name, Kind: PatchKind, URI: "coral://artifacts/" + digest, Digest: digest, Revision: "base1"}
}

// inReview takes a task with one patch to an open review round with the given reviewers.
func (e *verEnv) inReview(name string, art []TaskArtifact, reviewers ...string) int64 {
	e.t.Helper()
	task, err := e.s.CreateTask(e.ctx, "team", name, "", "medium", "lead", "worker")
	require.NoError(e.t, err)
	_, err = e.s.ClaimTask(e.ctx, "team", "worker", task.ID)
	require.NoError(e.t, err)
	_, err = e.s.SubmitCompletionReview(e.ctx, "team", task.ID, "worker", "built", "success", "needs review", art)
	require.NoError(e.t, err)
	_, _, err = e.s.HandToOrchestrator(e.ctx, "team", task.ID, "worker")
	require.NoError(e.t, err)
	_, _, err = e.s.DelegateReview(e.ctx, "team", task.ID, "lead", reviewers)
	require.NoError(e.t, err)
	return task.ID
}

func lastRound(t *testing.T, task *Task) ReviewRound {
	t.Helper()
	require.NotEmpty(t, task.Workflow.ReviewRounds)
	return task.Workflow.ReviewRounds[len(task.Workflow.ReviewRounds)-1]
}

func TestApprovalRecordsTheDigestsOfTheCurrentPatches(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1"), {Name: "notes", Content: "x"}}, "rev1")
	v, err := e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 0)
	require.NoError(t, err)
	require.True(t, v.Complete)
	assert.Equal(t, []string{"d1"}, lastRound(t, e.task(id)).ApprovedDigests, "only patch rows, by digest")
}

func TestAttachAddsARevisionAndIsIdempotent(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	task, reopened, err := e.s.AttachPatches(e.ctx, "team", id, "rev1", []TaskArtifact{patchRow("fix", "d2")})
	require.NoError(t, err)
	require.NotNil(t, reopened, "a new round: a reviewer who read the old bytes cannot approve these")
	assert.Equal(t, 2, reopened.Round)
	rows := task.Workflow.CompletionReview.Artifacts
	require.Len(t, rows, 2)
	assert.Equal(t, 1, rows[1].Seq)
	assert.Equal(t, "rev1", rows[1].AttachedBy)
	// the same name and digest again adds nothing, and opens no further round
	task, again, err := e.s.AttachPatches(e.ctx, "team", id, "rev1", []TaskArtifact{patchRow("fix", "d2")})
	require.NoError(t, err)
	assert.Nil(t, again)
	assert.Len(t, task.Workflow.CompletionReview.Artifacts, 2)
	assert.Len(t, task.Workflow.ReviewRounds, 2)
	// the queue's view of the task: the newest row of the name
	cur := currentPatches(task.Workflow.CompletionReview.Artifacts)
	require.Len(t, cur, 1)
	assert.Equal(t, "d2", cur[0].Digest)
}

func TestAttachIgnoresStampsFromTheClient(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	forged := patchRow("fix2", "d9")
	forged.Seq, forged.AttachedBy = 99, "someone-else"
	task, _, err := e.s.AttachPatches(e.ctx, "team", id, "worker", []TaskArtifact{forged})
	require.NoError(t, err)
	got := task.Workflow.CompletionReview.Artifacts[1]
	assert.Equal(t, 1, got.Seq, "the server numbers the row")
	assert.Equal(t, "worker", got.AttachedBy, "and names who attached it")
}

func TestAttachRefusesWhatIsNotAPatchWithItsFields(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	bad := patchRow("fix", "d2")
	bad.Revision = ""
	_, _, err := e.s.AttachPatches(e.ctx, "team", id, "rev1", []TaskArtifact{bad})
	require.ErrorContains(t, err, "needs uri, digest and revision")
	_, _, err = e.s.AttachPatches(e.ctx, "team", id, "rev1", nil)
	require.ErrorContains(t, err, "nothing to attach")
}

func TestAttachAfterAnApprovalSupersedesTheRound(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1", "rev2")
	_, err := e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 0)
	require.NoError(t, err)
	task, reopened, err := e.s.AttachPatches(e.ctx, "team", id, "rev2", []TaskArtifact{patchRow("fix", "d2")})
	require.NoError(t, err)
	require.NotNil(t, reopened)
	assert.Equal(t, 2, reopened.Round)
	require.Len(t, task.Workflow.ReviewRounds, 2)
	assert.Equal(t, "superseded", task.Workflow.ReviewRounds[0].Result)
	for _, r := range reopened.Reviewers {
		assert.Equal(t, "pending", r.State, "an approval of the old bytes does not carry over")
	}
	// a verdict that names the old round is refused, and the task is still in review, not completed
	_, err = e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 1)
	require.ErrorContains(t, err, "was resubmitted")
	assert.Equal(t, "review_pending", e.task(id).Status)
	// the new round needs both reviewers again; what it approved is the new digest, not the old one
	_, err = e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 2)
	require.NoError(t, err)
	v, err := e.s.ApproveReview(e.ctx, "team", id, "rev2", "ok", 2)
	require.NoError(t, err)
	require.True(t, v.Complete)
	assert.Equal(t, []string{"d2"}, lastRound(t, e.task(id)).ApprovedDigests)
}

func TestAReviewerCannotApproveTheirOwnPatchAlone(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	_, _, err := e.s.AttachPatches(e.ctx, "team", id, "rev1", []TaskArtifact{patchRow("fix", "d2")})
	require.NoError(t, err)
	_, err = e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 0)
	require.ErrorContains(t, err, "another reviewer has to approve it")
	// with a second reviewer on the round the first can approve, and the round completes only when both have
	_, _, err = e.s.DelegateReview(e.ctx, "team", id, "lead", []string{"rev1", "rev2"})
	require.NoError(t, err)
	v, err := e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 0)
	require.NoError(t, err)
	assert.False(t, v.Complete, "rev2 has still to approve it")
	v, err = e.s.ApproveReview(e.ctx, "team", id, "rev2", "ok", 0)
	require.NoError(t, err)
	assert.True(t, v.Complete)
}

func TestAttachIsOnlyForARoundInReview(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	v, err := e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 0)
	require.NoError(t, err)
	e.finish(v)
	_, _, err = e.s.AttachPatches(e.ctx, "team", id, "rev1", []TaskArtifact{patchRow("fix", "d2")})
	require.ErrorContains(t, err, "send the task back with task send-back")
	// a stranger is refused on an open round
	id2 := e.inReview("q", []TaskArtifact{patchRow("fix", "d3")}, "rev1")
	_, err = e.s.Subscribe(e.ctx, "team", "outsider", "Developer", "outsider-session", nil, "all")
	require.NoError(t, err)
	_, _, err = e.s.AttachPatches(e.ctx, "team", id2, "outsider", []TaskArtifact{patchRow("fix", "d4")})
	require.ErrorContains(t, err, "only the author, a reviewer of the round or the orchestrator")
}

func TestTheCapCountsTheNewestRowOfEachName(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d0")}, "rev1")
	for i := 1; i <= maxTaskArtifacts+5; i++ { // far more revisions than the cap: one name, so they all fit
		_, _, err := e.s.AttachPatches(e.ctx, "team", id, "worker", []TaskArtifact{patchRow("fix", "d"+string(rune('a'+i%26))+string(rune('a'+i/26)))})
		require.NoError(t, err, "revision %d", i)
	}
	var many []TaskArtifact
	for i := 0; i < maxTaskArtifacts; i++ {
		many = append(many, patchRow("other"+string(rune('a'+i%26))+string(rune('a'+i/26)), "x"+string(rune('a'+i%26))+string(rune('a'+i/26))))
	}
	_, _, err := e.s.AttachPatches(e.ctx, "team", id, "worker", many)
	require.ErrorContains(t, err, "at most")
}

func TestSendBackReturnsAnApprovedTaskToItsAuthor(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	_, err := e.s.SendBack(e.ctx, "team", id, "lead", "x")
	require.ErrorContains(t, err, "is not an approved, completed task", "an open round is not sent back")
	v, err := e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 0)
	require.NoError(t, err)
	e.finish(v)
	require.Equal(t, "completed", e.task(id).Status)
	_, err = e.s.SendBack(e.ctx, "team", id, "worker", "conflicts in a.go")
	require.ErrorContains(t, err, "orchestrator is required", "a developer cannot send a task back")
	_, err = e.s.SendBack(e.ctx, "team", id, "lead", " ")
	require.ErrorContains(t, err, "needs --note")
	task, err := e.s.SendBack(e.ctx, "team", id, "lead", "conflicts in a.go")
	require.NoError(t, err)
	assert.Equal(t, "pending", task.Status)
	require.NotNil(t, task.AssignedTo)
	assert.Equal(t, "worker", *task.AssignedTo)
	assert.Nil(t, task.Workflow.CompletionReview)
	assert.Empty(t, task.Workflow.Artifacts)
	r := lastRound(t, task)
	assert.Equal(t, "rejected", r.Result)
	assert.Equal(t, "lead", r.SentBackBy)
	assert.Equal(t, "conflicts in a.go", r.SentBackNote)
	// the author resubmits and the same reviewers are asked again, as after a rejection
	_, err = e.s.SubmitCompletionReview(e.ctx, "team", id, "worker", "fixed", "success", "again", []TaskArtifact{patchRow("fix", "d2")})
	require.NoError(t, err)
	_, reopened, err := e.s.HandToOrchestrator(e.ctx, "team", id, "worker")
	require.NoError(t, err)
	require.NotNil(t, reopened)
	assert.Equal(t, 2, reopened.Round)
}

// The approving completion carries every revision of a patch (the same name more than once): it was refused as a duplicate name.
func TestAnApprovedTaskKeepsEveryRevisionAndApprovesTheNewest(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	_, _, err := e.s.AttachPatches(e.ctx, "team", id, "worker", []TaskArtifact{patchRow("fix", "d2")})
	require.NoError(t, err)
	v, err := e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 0)
	require.NoError(t, err)
	e.finish(v)
	done := e.task(id)
	require.Equal(t, "completed", done.Status)
	require.Len(t, done.Workflow.Artifacts, 2, "both revisions stay on the task")
	assert.Equal(t, []string{"d2"}, lastRound(t, done).ApprovedDigests, "only the newest is approved")
	// a client that sends the same name twice in one submission is still refused
	_, err = validateTaskArtifacts([]TaskArtifact{{Name: "x", Content: "1"}, {Name: "x", Content: "2"}})
	require.ErrorContains(t, err, "unique")
}

// An attach and the last approval race (Orchestrator, Backend's #5689 review): whichever order they come in, the task never completes
// with a row nobody approved, and no one approves bytes they did not see.
func TestAnAttachRacingTheLastApprovalNeverCompletesWithAnUnapprovedRow(t *testing.T) {
	t.Run("approval first", func(t *testing.T) {
		e := newVerEnv(t)
		id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
		v, err := e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 1)
		require.NoError(t, err)
		e.finish(v)
		_, _, err = e.s.AttachPatches(e.ctx, "team", id, "worker", []TaskArtifact{patchRow("fix", "d2")})
		require.ErrorContains(t, err, "not in review with an open round")
		assert.Equal(t, []string{"d1"}, lastRound(t, e.task(id)).ApprovedDigests)
	})
	t.Run("attach first", func(t *testing.T) {
		e := newVerEnv(t)
		id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
		_, _, err := e.s.AttachPatches(e.ctx, "team", id, "worker", []TaskArtifact{patchRow("fix", "d2")})
		require.NoError(t, err)
		_, err = e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 1) // the reviewer read round 1
		require.ErrorContains(t, err, "was resubmitted")
		assert.NotEqual(t, "completed", e.task(id).Status)
		v, err := e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 2) // after reading round 2
		require.NoError(t, err)
		assert.True(t, v.Complete)
		assert.Equal(t, []string{"d2"}, lastRound(t, e.task(id)).ApprovedDigests)
	})
	t.Run("at the same time", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			e := newVerEnv(t)
			id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
			var wg sync.WaitGroup
			var approved *ReviewVerdict
			var aerr, perr error
			wg.Add(2)
			go func() { defer wg.Done(); approved, aerr = e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 1) }()
			go func() {
				defer wg.Done()
				_, _, perr = e.s.AttachPatches(e.ctx, "team", id, "worker", []TaskArtifact{patchRow("fix", "d2")})
			}()
			wg.Wait()
			final := lastRound(t, e.task(id))
			if aerr == nil && approved.Complete {
				// the approval won: the attach was refused and what was approved is the old row only
				require.Error(t, perr)
				assert.Equal(t, []string{"d1"}, final.ApprovedDigests)
			} else {
				// the attach won: the approval of round 1 was refused and nothing is approved
				require.NoError(t, perr)
				require.Error(t, aerr)
				assert.Empty(t, final.ApprovedDigests)
			}
		}
	})
}

// A client's Seq and AttachedBy are never kept (Backend's #5689 must-fix): they could otherwise open the cap and the unique-name check.
func TestAClientCannotUseTheServersFieldsToOpenTheCapOrTheNames(t *testing.T) {
	e := newVerEnv(t)
	// two rows of one name in one attach are refused whatever Seq they carry
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	a, b := patchRow("fix", "d2"), patchRow("fix", "d3")
	a.Seq, b.Seq = 5, 6
	_, _, err := e.s.AttachPatches(e.ctx, "team", id, "worker", []TaskArtifact{a, b})
	require.ErrorContains(t, err, "unique")
	// an AttachedBy sent on an attach is replaced by the actor
	forged := patchRow("fix", "d4")
	forged.AttachedBy = "rev2"
	got, _, err := e.s.AttachPatches(e.ctx, "team", id, "worker", []TaskArtifact{forged})
	require.NoError(t, err)
	rows := got.Workflow.CompletionReview.Artifacts
	assert.Equal(t, "worker", rows[len(rows)-1].AttachedBy)
	// 33 rows of one name, each claiming a Seq, are refused on complete
	var many []TaskArtifact
	for i := 0; i < maxTaskArtifacts+1; i++ {
		many = append(many, TaskArtifact{Name: "a", Content: "x", Seq: 1, AttachedBy: "forged"})
	}
	task, err := e.s.CreateTask(e.ctx, "team", "c", "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = e.s.ClaimTask(e.ctx, "team", "worker", task.ID)
	require.NoError(t, err)
	_, err = e.s.CompleteTaskWithArtifactsAtRevisionAndCandidate(e.ctx, "team", task.ID, "worker", nil, "success", many, nil, "")
	require.Error(t, err)
	// and on a complete the stamps are cleared
	_, err = e.s.CompleteTaskWithArtifactsAtRevisionAndCandidate(e.ctx, "team", task.ID, "worker", nil, "success", []TaskArtifact{{Name: "r", Content: "x", Seq: 9, AttachedBy: "forged"}}, nil, "")
	require.NoError(t, err)
	art := e.task(task.ID).Workflow.Artifacts[0]
	assert.Zero(t, art.Seq)
	assert.Empty(t, art.AttachedBy)
}

func TestSendBackKeepsTheDigestsItSentBack(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	v, err := e.s.ApproveReview(e.ctx, "team", id, "rev1", "ok", 0)
	require.NoError(t, err)
	e.finish(v)
	task, err := e.s.SendBack(e.ctx, "team", id, "lead", "again")
	require.NoError(t, err)
	assert.Equal(t, []string{"d1"}, lastRound(t, task).SentBackDigests)
}
