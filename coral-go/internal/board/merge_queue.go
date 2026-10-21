package board

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/subgentic/coral/internal/ptr"
)

// The merge queue's board side (#5689). A patch is a TaskArtifact of kind "patch": uri (the managed link), digest (the file's
// sha256), revision (the base commit it applies to). The review rounds say WHICH bytes were approved (ReviewRound.ApprovedDigests),
// so a queue never lands bytes that were attached after an approval.

// PatchKind is the TaskArtifact kind of a patch the merge queue can land.
const PatchKind = "patch"

// stampArtifacts sets the server's own fields on rows recorded by actor: the order (Seq, counted on from after) and who attached them.
func stampArtifacts(rows []TaskArtifact, actor string, after int) {
	for i := range rows {
		after++
		rows[i].Seq, rows[i].AttachedBy = after, actor
	}
}

// clearStamps drops what a client sent in the server's fields.
func clearStamps(rows []TaskArtifact) {
	for i := range rows {
		rows[i].Seq, rows[i].AttachedBy, rows[i].LandedBy, rows[i].LandedAt = 0, "", "", ""
	}
}

func maxSeq(rows []TaskArtifact) int {
	m := 0
	for _, r := range rows {
		if r.Seq > m {
			m = r.Seq
		}
	}
	return m
}

// newestByName keeps, of the rows with the same name, only the one recorded last (the highest Seq; the later row among equals).
func newestByName(rows []TaskArtifact) []TaskArtifact {
	last := map[string]int{}
	for i, r := range rows {
		if j, ok := last[r.Name]; !ok || r.Seq >= rows[j].Seq {
			last[r.Name] = i
		}
	}
	var out []TaskArtifact
	for i, r := range rows {
		if last[r.Name] == i {
			out = append(out, r)
		}
	}
	return out
}

// currentPatches are the patch rows a queue would land: the newest row of every patch name.
func currentPatches(rows []TaskArtifact) []TaskArtifact {
	var out []TaskArtifact
	for _, r := range newestByName(rows) {
		if r.Kind == PatchKind {
			out = append(out, r)
		}
	}
	return out
}

// patchDigests are the digests of a candidate's current patches, sorted.
func patchDigests(cr *CompletionReview) []string {
	if cr == nil {
		return nil
	}
	var out []string
	for _, p := range currentPatches(cr.Artifacts) {
		out = append(out, p.Digest)
	}
	sort.Strings(out)
	return out
}

// checkOwnPatchApproval: a reviewer who attached a patch row cannot approve it alone (#5689): another reviewer of the round has to
// approve it, so a round with that reviewer as its only reviewer is refused with the way out. With two or more reviewers the attacher
// may approve their own fix: every reviewer of the round has to approve for it to complete, so another one still approves the same
// bytes. It is not "never your own patch", it is "never only you".
func checkOwnPatchApproval(id int64, w *TaskWorkflow, round *ReviewRound, actor string) error {
	if w.CompletionReview == nil {
		return nil
	}
	own := false
	for _, p := range currentPatches(w.CompletionReview.Artifacts) {
		if p.AttachedBy == actor {
			own = true
		}
	}
	if !own {
		return nil
	}
	for _, r := range round.Reviewers {
		if r.Reviewer != actor {
			return nil
		}
	}
	return fmt.Errorf("task #%d has a patch you attached yourself: another reviewer has to approve it (ask the orchestrator to add one with task review)", id)
}

// AttachPatches adds rows to a task that is in review with an open round (#5689): a reviewer's fix commit, or the author's next
// revision of a patch. A row whose name and digest are already there is skipped, so a retry adds nothing. A row with a name the task
// already has is a newer revision of that patch. The round is superseded and the same reviewers review the new bytes in a new round
// (an approval, or a verdict that is about to be given, never carries over to different digests). reopened is that new round, nil
// when nothing was added (a retry).
func (s *Store) AttachPatches(ctx context.Context, project string, id int64, actor string, rows []TaskArtifact) (task *Task, reopened *ReviewRound, err error) {
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("nothing to attach")
	}
	clearStamps(rows) // a client's Seq and AttachedBy are replaced, and checked as 0 below: two rows of one name in one attach are refused
	if _, err := validateTaskArtifacts(rows); err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if r.Kind == PatchKind && (strings.TrimSpace(r.Digest) == "" || strings.TrimSpace(r.Revision) == "" || strings.TrimSpace(r.URI) == "") {
			return nil, nil, fmt.Errorf("patch %q needs uri, digest and revision (the base commit it applies to)", r.Name)
		}
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.GetContext(ctx, &status, "SELECT status FROM board_tasks WHERE id=? AND board_id=?", id, project); err != nil {
		return nil, nil, fmt.Errorf("task #%d is not on this board", id)
	}
	w, err := loadWorkflow(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	n := len(w.ReviewRounds)
	if status != "review_pending" || n == 0 || w.ReviewRounds[n-1].Result != "" || w.CompletionReview == nil {
		return nil, nil, fmt.Errorf("task #%d is not in review with an open round: attach works only then (after completion, send the task back with task send-back)", id)
	}
	round := &w.ReviewRounds[n-1]
	allowed := actor == w.OriginalAuthor
	for _, r := range round.Reviewers {
		if r.Reviewer == actor {
			allowed = true
		}
	}
	if !allowed && s.RequireTaskReviewer(ctx, project, actor) == nil {
		allowed = true
	}
	if !allowed {
		return nil, nil, fmt.Errorf("only the author, a reviewer of the round or the orchestrator can attach to task #%d", id)
	}
	have := w.CompletionReview.Artifacts
	var fresh []TaskArtifact
	for _, r := range rows {
		dup := false
		for _, h := range have {
			if h.Name == r.Name && h.Digest == r.Digest && r.Digest != "" {
				dup = true
			}
		}
		if !dup {
			fresh = append(fresh, r)
		}
	}
	if len(fresh) == 0 {
		t, err := s.getTaskByID(ctx, project, id)
		return t, nil, err
	}
	stampArtifacts(fresh, actor, maxSeq(have))
	all := append(append([]TaskArtifact{}, have...), fresh...)
	if len(newestByName(all)) > maxTaskArtifacts {
		return nil, nil, fmt.Errorf("a task has at most %d artifacts (the newest revision of each name counts)", maxTaskArtifacts)
	}
	w.CompletionReview.Artifacts = all
	// Always a new round (not only after an approval): a reviewer may have read the task before this attach and be about to approve it,
	// and a verdict that names the round they read is then refused (task detail shows the round), so no one approves bytes they did not see.
	round.Result = "superseded"
	if reopened, err = reopenReviews(ctx, tx, project, id, &w); err != nil {
		return nil, nil, err
	}
	reopened.DelegatedBy = "superseded by a new patch from " + actor
	if err := saveWorkflow(ctx, tx, id, w); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	task, err = s.getTaskByID(ctx, project, id)
	return task, reopened, err
}

// SendBack returns a task that was approved and completed to its author (#5689), with the note: what the queue does with a patch it
// cannot land, and the way to ask for a fix after approval. The completion is undone for the task itself (status, outcome, the
// recorded artifacts); what the completion already did to tasks that waited on it is not undone. The last round is recorded as
// rejected with the note, so the author's resubmission reopens the same reviewers like any rejected task.
func (s *Store) SendBack(ctx context.Context, project string, id int64, actor, note string) (*Task, error) {
	if strings.TrimSpace(note) == "" || len(note) > 4096 {
		return nil, fmt.Errorf("say why: a send-back needs --note (4096 characters at most)")
	}
	if err := s.RequireTaskReviewer(ctx, project, actor); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.GetContext(ctx, &status, "SELECT status FROM board_tasks WHERE id=? AND board_id=?", id, project); err != nil {
		return nil, fmt.Errorf("task #%d is not on this board", id)
	}
	w, err := loadWorkflow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	n := len(w.ReviewRounds)
	if status != "completed" || n == 0 || w.ReviewRounds[n-1].Result != "approved" || w.OriginalAuthor == "" {
		return nil, fmt.Errorf("task #%d is not an approved, completed task: send-back applies to those only", id)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE board_tasks SET status='pending', assigned_to=?, claimed_at=NULL, completed_by=NULL, completion_message=NULL,
		completed_at=NULL WHERE id=? AND board_id=? AND status='completed'`, ptr.NullIfEmpty(w.OriginalAuthor), id, project); err != nil {
		return nil, err
	}
	last := &w.ReviewRounds[n-1]
	last.Result, last.SentBackBy, last.SentBackNote = "rejected", actor, note
	last.SentBackDigests = patchDigests(w.CompletionReview)
	w.Outcome, w.Artifacts, w.CompletionReview = "", nil, nil
	if err := saveWorkflow(ctx, tx, id, w); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getTaskByID(ctx, project, id)
}
