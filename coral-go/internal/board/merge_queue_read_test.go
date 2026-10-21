package board

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	sha1c = "1111111111111111111111111111111111111111"
	sha2c = "2222222222222222222222222222222222222222"
)

// approve runs the task's single reviewer's approval to completion.
func (e *verEnv) approve(id int64, reviewer string) {
	e.t.Helper()
	v, err := e.s.ApproveReview(e.ctx, "team", id, reviewer, "ok", 0)
	require.NoError(e.t, err)
	require.True(e.t, v.Complete)
	e.finish(v)
}

func queueDigests(t *testing.T, e *verEnv) map[int64][]string {
	t.Helper()
	q, err := e.s.MergeQueue(e.ctx, "team")
	require.NoError(t, err)
	out := map[int64][]string{}
	for _, entry := range q {
		for _, p := range entry.Patches {
			out[entry.TaskID] = append(out[entry.TaskID], p.Digest)
		}
	}
	return out
}

func TestTheQueueListsApprovedTasksOldestApprovalFirst(t *testing.T) {
	e := newVerEnv(t)
	a := e.inReview("a", []TaskArtifact{patchRow("fix", "da")}, "rev1")
	b := e.inReview("b", []TaskArtifact{patchRow("fix", "db")}, "rev1")
	open := e.inReview("c", []TaskArtifact{patchRow("fix", "dc")}, "rev1") // never approved
	e.approve(b, "rev1")
	e.approve(a, "rev1")
	// the clock has a one-second grain: say which round concluded first
	conclude := func(id int64, at string) {
		e.editWorkflow(id, func(w *TaskWorkflow) { w.ReviewRounds[len(w.ReviewRounds)-1].ConcludedAt = at })
	}
	conclude(b, "2020-01-01T00:00:00Z")
	conclude(a, "2020-01-02T00:00:00Z")
	q, err := e.s.MergeQueue(e.ctx, "team")
	require.NoError(t, err)
	require.Len(t, q, 2, "a task still in review is not in the queue")
	assert.NotContains(t, queueDigests(t, e), open)
	assert.Equal(t, b, q[0].TaskID, "oldest approval first, not lowest id")
	assert.Equal(t, a, q[1].TaskID)
	assert.Equal(t, []string{"rev1"}, q[0].Approvers)
	assert.Equal(t, "worker", q[0].Author)
	assert.NotEmpty(t, q[0].ApprovedAt)
	conclude(a, "2019-01-01T00:00:00Z")
	q, err = e.s.MergeQueue(e.ctx, "team")
	require.NoError(t, err)
	assert.Equal(t, a, q[0].TaskID, "the order follows the round's concluded time")
}

// The queue shows the newest row of each patch name that its approving round approved: a superseded row is history, and a row nobody
// approved is never a candidate, not even in place of the older approved one.
func TestTheQueueNeverShowsASupersededOrAnUnapprovedRow(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	_, _, err := e.s.AttachPatches(e.ctx, "team", id, "worker", []TaskArtifact{patchRow("fix", "d2")})
	require.NoError(t, err)
	e.approve(id, "rev1")
	assert.Equal(t, []string{"d2"}, queueDigests(t, e)[id], "d1 stays on the task as history, the queue shows d2")
	assert.Len(t, e.task(id).Workflow.Artifacts, 2)

	// a newer row that no round approved (a burst, or a write after the approval) takes the name: nothing of this task is a candidate
	e.editWorkflow(id, func(w *TaskWorkflow) {
		w.Artifacts = append(w.Artifacts, TaskArtifact{Name: "fix", Kind: PatchKind, URI: "coral://artifacts/d9", Digest: "d9", Revision: "base1", Seq: 9})
	})
	assert.NotContains(t, queueDigests(t, e), id, "the newest row is not in approved_digests: neither it nor the older approved row is landed")
}

func TestAClientsLandedFlagAndStampsDoNotHideOrForgeARow(t *testing.T) {
	e := newVerEnv(t)
	row := patchRow("fix", "d1")
	row.Landed, row.LandedBy, row.LandedAt = true, "someone", "2000-01-01T00:00:00Z"
	id := e.inReview("p", []TaskArtifact{row}, "rev1")
	e.approve(id, "rev1")
	got := e.task(id).Workflow.Artifacts[0]
	assert.Empty(t, got.LandedBy, "landed_by is the server's: a client's value is dropped")
	assert.Empty(t, got.LandedAt)
	assert.Equal(t, []string{"d1"}, queueDigests(t, e)[id], "the client-writable landed flag does not take it out of the queue")
}

func TestMarkLandedRecordsWhatLandedAndEmptiesTheQueue(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1"), patchRow("docs", "d2")}, "rev1")
	e.approve(id, "rev1")
	task, err := e.s.MarkLanded(e.ctx, "team", id, "lead", false, []string{"d1"}, []LandedCommit{{SHA: sha1c, Subject: "the fix"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"d2"}, queueDigests(t, e)[id], "only the landed patch leaves; the other stays queued")
	var landed, commits int
	for _, a := range task.Workflow.Artifacts {
		if a.Kind == PatchKind && a.LandedBy == "lead" && a.LandedAt != "" {
			landed++
			assert.Equal(t, "d1", a.Digest)
		}
		if a.Kind == landedCommitKind {
			commits++
			assert.Equal(t, sha1c, a.Revision)
			assert.Equal(t, "the fix", a.Content)
			assert.Equal(t, "lead", a.LandedBy)
		}
	}
	assert.Equal(t, 1, landed)
	assert.Equal(t, 1, commits)
	// idempotent: the marker and the commit rows are not duplicated or replaced
	task, err = e.s.MarkLanded(e.ctx, "team", id, "lead", false, []string{"d1"}, []LandedCommit{{SHA: sha1c, Subject: "the fix"}, {SHA: sha2c}})
	require.NoError(t, err)
	commits = 0
	for _, a := range task.Workflow.Artifacts {
		if a.Kind == landedCommitKind {
			commits++
		}
	}
	assert.Equal(t, 2, commits, "the known commit is not added again, the new one is")
	_, err = e.s.MarkLanded(e.ctx, "team", id, "lead", false, []string{"d2"}, nil)
	require.NoError(t, err)
	assert.NotContains(t, queueDigests(t, e), id, "everything landed: the task leaves the queue")
}

func TestMarkLandedRefusesWhatIsNotApprovedOrNotAllowed(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	_, err := e.s.MarkLanded(e.ctx, "team", id, "lead", false, []string{"d1"}, nil)
	require.ErrorContains(t, err, "not an approved, completed task", "a task still in review")
	e.approve(id, "rev1")
	// the author, a reviewer and a stranger cannot mark it landed; the orchestrator and the Operator can
	for _, who := range []string{"worker", "rev1", "outsider"} {
		_, err = e.s.MarkLanded(e.ctx, "team", id, who, false, []string{"d1"}, nil)
		require.ErrorContains(t, err, "orchestrator is required", who)
	}
	assert.Equal(t, []string{"d1"}, queueDigests(t, e)[id], "nothing was recorded")
	// a digest the round did not approve, or no digest at all, or a bad sha
	_, err = e.s.MarkLanded(e.ctx, "team", id, "lead", false, []string{"d9"}, nil)
	require.ErrorContains(t, err, "not an approved patch")
	_, err = e.s.MarkLanded(e.ctx, "team", id, "lead", false, []string{"d1", "d9"}, nil)
	require.ErrorContains(t, err, "not an approved patch", "one bad digest refuses the whole call")
	_, err = e.s.MarkLanded(e.ctx, "team", id, "lead", false, nil, nil)
	require.Error(t, err)
	_, err = e.s.MarkLanded(e.ctx, "team", id, "lead", false, []string{"d1"}, []LandedCommit{{SHA: "abc"}})
	require.ErrorContains(t, err, "40-digit")
	_, err = e.s.MarkLanded(e.ctx, "team", id, "lead", false, []string{"d1"}, []LandedCommit{{SHA: sha1c, Subject: "bad\x07subject"}})
	require.Error(t, err)
	_, err = e.s.MarkLanded(e.ctx, "team", id, "lead", false, []string{"d1"}, make([]LandedCommit, maxLandedCommits+1))
	require.Error(t, err)
	assert.Equal(t, []string{"d1"}, queueDigests(t, e)[id], "every refusal recorded nothing")
	_, err = e.s.MarkLanded(e.ctx, "team", id, "Operator", true, []string{"d1"}, nil)
	require.NoError(t, err, "the Operator may")
	assert.NotContains(t, queueDigests(t, e), id)
}

// An older, superseded digest is not a patch of the task any more: it cannot be marked landed to hide the newest.
func TestMarkLandedRefusesASupersededDigest(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	_, _, err := e.s.AttachPatches(e.ctx, "team", id, "worker", []TaskArtifact{patchRow("fix", "d2")})
	require.NoError(t, err)
	e.approve(id, "rev1")
	_, err = e.s.MarkLanded(e.ctx, "team", id, "lead", false, []string{"d1"}, nil)
	require.ErrorContains(t, err, "not an approved patch")
	assert.True(t, strings.Contains(strings.Join(queueDigests(t, e)[id], ","), "d2"))
}
