package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/subgentic/coral/internal/ptr"
)

// The review flow (#3725, settled with the operator):
//  1. submit-review gives the task to the team's orchestrator (status review_pending) and records
//     its original author; with no orchestrator on the team it completes the task.
//  2. The orchestrator completes it, or delegates it: one review task per reviewer, an ordinary
//     task in the reviewer's queue, claimed like any other (it takes their one slot).
//  3. Each reviewer approves or rejects it, any time it is in review, on their review task. When
//     every reviewer of the round has approved, the task completes itself. A rejection sends it
//     back to its author (pending, with the note); the reviewers stay on it (#3787).
//  4. When the author submits it again (after a rejection, re-claimed or not), the same reviewers'
//     review tasks reopen for the new round, with no orchestrator step: every reviewer, the
//     rejecter and earlier approvers too, reviews the new revision. The orchestrator can still
//     add or drop reviewers with task review. A verdict naming an earlier round is refused.

// ReviewRound is one delegation of a task to reviewers.
type ReviewRound struct {
	Round       int             `json:"round"`
	DelegatedBy string          `json:"delegated_by"`
	DelegatedAt string          `json:"delegated_at"`
	Reviewers   []ReviewerState `json:"reviewers"`
	Result      string          `json:"result,omitempty"` // "", approved, rejected or superseded (a patch was attached after an approval, #5689)
	// ConcludedAt: when the last reviewer approved the round (the merge queue lands oldest first by it, #5689).
	ConcludedAt string `json:"concluded_at,omitempty"`
	// ApprovedDigests: when the round was approved, the digest of the newest row of every patch the task had (#5689). The merge
	// queue lands a patch only if its digest is here, so bytes attached after an approval are never landed on its strength.
	ApprovedDigests []string `json:"approved_digests,omitempty"`
	// SentBackBy and SentBackNote: an approved task returned to its author by task send-back (#5689); Result is then "rejected",
	// so the resubmission path is the one a rejection uses.
	SentBackBy   string `json:"sent_back_by,omitempty"`
	SentBackNote string `json:"sent_back_note,omitempty"`
	// SupersededBy: who attached the patch that opened this round (#5689): the burst rule lets their next attach join it.
	SupersededBy string `json:"superseded_by,omitempty"`
	// SentBackDigests: the patches that were approved and sent back, kept for the history (the queue never reads it).
	SentBackDigests []string `json:"sent_back_digests,omitempty"`
}

// ReviewerState is one reviewer's part in a round.
type ReviewerState struct {
	Reviewer     string `json:"reviewer"`
	ReviewTaskID int64  `json:"review_task_id"`
	State        string `json:"state"` // pending, approved or rejected
	Note         string `json:"note,omitempty"`
	At           string `json:"at,omitempty"`
}

// ErrNoOrchestrator: the team has no active orchestrator to review the task.
var ErrNoOrchestrator = errors.New("the team has no active orchestrator")

// TeamOrchestrator is the board's active orchestrator subscriber.
func (s *Store) TeamOrchestrator(ctx context.Context, project string) (string, error) {
	subs, err := s.ListSubscribers(ctx, project)
	if err != nil {
		return "", err
	}
	for _, sub := range subs {
		if sub.IsActive != 0 && strings.EqualFold(strings.TrimSpace(sub.JobTitle), "Orchestrator") {
			return sub.SubscriberID, nil
		}
	}
	return "", ErrNoOrchestrator
}

// HandToOrchestrator moves a task whose candidate was just recorded (submit-review) to the
// orchestrator: status review_pending, assigned to it, the author recorded. The author's slot is
// free at once. ErrNoOrchestrator when the team has none (the caller then completes the task).
// When the task was already reviewed and sent back (a rejection), its reviewers' review tasks
// reopen for a new round (#3787): reopened is that round, nil on a first submission.
func (s *Store) HandToOrchestrator(ctx context.Context, project string, id int64, author string) (orch string, reopened *ReviewRound, err error) {
	orch, err = s.TeamOrchestrator(ctx, project)
	if err != nil {
		return "", nil, err
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	w, err := loadWorkflow(ctx, tx, id)
	if err != nil {
		return "", nil, err
	}
	// In progress, or pending with its author after a rejection (resubmitted unclaimed, #3889).
	res, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status='review_pending', assigned_to=? WHERE id=? AND board_id=? AND (status='in_progress' OR (? AND status='pending' AND assigned_to=?))",
		orch, id, project, lastRoundRejected(w), author)
	if err != nil {
		return "", nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", nil, fmt.Errorf("task #%d is not in progress on this board", id)
	}
	w.OriginalAuthor = author
	if w.CompletionReview != nil {
		w.CompletionReview.ReleasedBy, w.CompletionReview.ReleasedAt, w.CompletionReview.ReleaseReason = "Coral", nowUTC(), "submitted for review: given to the orchestrator"
	}
	if lastRoundRejected(w) {
		if reopened, err = reopenReviews(ctx, tx, project, id, &w); err != nil {
			return "", nil, err
		}
	}
	if err := saveWorkflow(ctx, tx, id, w); err != nil {
		return "", nil, err
	}
	return orch, reopened, tx.Commit()
}

// reopenReviews starts the next round with the last round's reviewers (#3787): each one's review
// task is reopened for it (pending, or left open as it is), its text names the new round, and
// every reviewer's state is pending again. Called with the task resubmitted after a rejection.
func reopenReviews(ctx context.Context, tx *sqlx.Tx, project string, id int64, w *TaskWorkflow) (*ReviewRound, error) {
	last := w.ReviewRounds[len(w.ReviewRounds)-1]
	var title string
	if err := tx.GetContext(ctx, &title, "SELECT title FROM board_tasks WHERE id=? AND board_id=?", id, project); err != nil {
		return nil, err
	}
	round := ReviewRound{Round: last.Round + 1, DelegatedBy: "resubmitted", DelegatedAt: nowUTC()}
	for _, r := range last.Reviewers {
		body := reviewTaskBody(id, title, w.OriginalAuthor, round.Round)
		if _, err := tx.ExecContext(ctx, `UPDATE board_tasks SET status=CASE WHEN status IN ('pending','in_progress') THEN status ELSE 'pending' END,
			completed_by=NULL, completion_message=NULL, completed_at=NULL, body=? WHERE id=? AND board_id=?`, body, r.ReviewTaskID, project); err != nil {
			return nil, err
		}
		rw, err := loadWorkflow(ctx, tx, r.ReviewTaskID)
		if err != nil {
			return nil, err
		}
		rw.Outcome, rw.ReviewRound = "", round.Round
		if err := saveWorkflow(ctx, tx, r.ReviewTaskID, rw); err != nil {
			return nil, err
		}
		round.Reviewers = append(round.Reviewers, ReviewerState{Reviewer: r.Reviewer, ReviewTaskID: r.ReviewTaskID, State: "pending"})
	}
	w.ReviewRounds = append(w.ReviewRounds, round)
	return &w.ReviewRounds[len(w.ReviewRounds)-1], nil
}

// reviewTaskBody is a review task's text for a round.
func reviewTaskBody(id int64, title, author string, round int) string {
	return fmt.Sprintf("Review task #%d, %q, for %s (round %d).\nRead it: coral-board task detail %d\nThen: coral-board task approve %d --round %d [--note \"...\"], or coral-board task reject %d --round %d --note \"why\".",
		id, title, author, round, id, id, round, id, round)
}

// DelegateReview assigns a task in review to reviewers (only the team's orchestrator, only a
// task in review): one review task each, in their queue. With a round open it changes that
// round's reviewers instead (#3787): a reviewer no longer named has their review task skipped, a
// new one gets a review task, the rest keep theirs and their verdicts. It refuses a reviewer who
// isn't an active subscriber, and the task's own author.
func (s *Store) DelegateReview(ctx context.Context, project string, id int64, actor string, reviewers []string) (*Task, []*Task, error) {
	if err := s.RequireTaskReviewer(ctx, project, actor); err != nil {
		return nil, nil, err
	}
	var clean []string
	seen := map[string]bool{}
	for _, r := range reviewers {
		if r = strings.TrimSpace(r); r != "" && !seen[r] {
			seen[r] = true
			clean = append(clean, r)
		}
	}
	if len(clean) == 0 {
		return nil, nil, fmt.Errorf("name at least one reviewer")
	}
	task, err := s.GetTask(ctx, project, id)
	if err != nil {
		return nil, nil, err
	}
	if task.Status != "review_pending" {
		return nil, nil, fmt.Errorf("task #%d is not in review", id)
	}
	w, err := loadWorkflow(ctx, s.db, id)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range clean {
		if strings.EqualFold(r, w.OriginalAuthor) {
			return nil, nil, fmt.Errorf("%s did the work and can't review it", r)
		}
		sub, err := s.GetProjectSubscription(ctx, project, r)
		if err != nil {
			return nil, nil, err
		}
		// GetProjectSubscription selects only is_active = 1 rows, so IsActive == 0 cannot be true here today: the check mirrors that SQL filter
		// and keeps the rule if the lookup ever returns inactive rows.
		if sub == nil || sub.IsActive == 0 {
			return nil, nil, fmt.Errorf("%s is not on this board", r)
		}
	}
	open := len(w.ReviewRounds) > 0 && w.ReviewRounds[len(w.ReviewRounds)-1].Result == ""
	round := ReviewRound{Round: len(w.ReviewRounds) + 1, DelegatedBy: actor, DelegatedAt: nowUTC()}
	have := map[string]ReviewerState{}
	if open {
		round = w.ReviewRounds[len(w.ReviewRounds)-1]
		for _, r := range round.Reviewers {
			have[r.Reviewer] = r
		}
		round.Reviewers = nil
		for _, r := range have {
			if !seen[r.Reviewer] { // dropped: their review ends
				if _, err := s.db.ExecContext(ctx, "UPDATE board_tasks SET status='skipped', completed_by=?, completion_message=?, completed_at=? WHERE id=? AND board_id=? AND status IN ('pending','in_progress')",
					actor, fmt.Sprintf("%s took %s off the review of #%d", actor, r.Reviewer, id), nowUTC(), r.ReviewTaskID, project); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	var created []*Task
	for _, r := range clean {
		if prev, ok := have[r]; ok {
			round.Reviewers = append(round.Reviewers, prev) // kept, with any verdict given
			continue
		}
		rt, err := s.CreateTaskWithOpts(ctx, project, fmt.Sprintf("Review #%d: %s", id, task.Title), reviewTaskBody(id, task.Title, w.OriginalAuthor, round.Round), task.Priority, actor,
			&CreateTaskOpts{Workflow: TaskWorkflow{ReviewOf: id, ReviewRound: round.Round}}, r)
		if err != nil {
			return nil, nil, err
		}
		created = append(created, rt)
		round.Reviewers = append(round.Reviewers, ReviewerState{Reviewer: r, ReviewTaskID: rt.ID, State: "pending"})
	}
	if open {
		w.ReviewRounds[len(w.ReviewRounds)-1] = round
	} else {
		w.ReviewRounds = append(w.ReviewRounds, round)
	}
	if err := saveWorkflow(ctx, s.db, id, w); err != nil {
		return nil, nil, err
	}
	task, err = s.GetTask(ctx, project, id)
	return task, created, err
}

// ReviewVerdict is what ApproveReview or RejectReview did.
type ReviewVerdict struct {
	Task         *Task  // the task under review
	ReviewTask   *Task  // the reviewer's review task, now completed
	Complete     bool   // every reviewer approved: complete Task (as the orchestrator)
	Rejected     bool   // sent back to its author
	Author       string // the original author
	Orchestrator string
	Approvers    []string // when Complete: the round's reviewers, in the order they were named
}

// ApproveReview records actor's approval on their review task (id may name the task under review
// or the review task). round, when not 0, is the round the reviewer reviewed: a verdict on an
// earlier round (the task was resubmitted since) is refused (#3787).
func (s *Store) ApproveReview(ctx context.Context, project string, id int64, actor, note string, round int) (*ReviewVerdict, error) {
	return s.reviewVerdict(ctx, project, id, actor, note, true, round)
}

// RejectReview records actor's rejection (a note is required): the task goes back to its author.
func (s *Store) RejectReview(ctx context.Context, project string, id int64, actor, note string, round int) (*ReviewVerdict, error) {
	if strings.TrimSpace(note) == "" {
		return nil, fmt.Errorf("say why: a rejection needs --note")
	}
	return s.reviewVerdict(ctx, project, id, actor, note, false, round)
}

func (s *Store) reviewVerdict(ctx context.Context, project string, id int64, actor, note string, approve bool, reviewed int) (*ReviewVerdict, error) {
	if len(note) > 4096 {
		return nil, fmt.Errorf("the note is too long (4096 characters at most)")
	}
	// id may be the review task: go to its parent.
	if w, err := loadWorkflow(ctx, s.db, id); err == nil && w.ReviewOf > 0 {
		id = w.ReviewOf
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.GetContext(ctx, &status, "SELECT status FROM board_tasks WHERE id=? AND board_id=?", id, project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("task #%d is not on this board", id)
		}
		return nil, err
	}
	w, err := loadWorkflow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := checkOpenReview(id, status, w); err != nil {
		return nil, err
	}
	round := &w.ReviewRounds[len(w.ReviewRounds)-1]
	me, err := roundReviewer(id, round, actor, reviewed)
	if err != nil {
		return nil, err
	}
	if approve {
		if err := checkOwnPatchApproval(id, &w, round, actor); err != nil {
			return nil, err
		}
	}
	if err := checkReviewTaskOpen(ctx, tx, project, id, me.ReviewTaskID, actor); err != nil {
		return nil, err
	}
	now := nowUTC()
	outcome, verdict := "success", "approved"
	if !approve {
		outcome, verdict = "failed", "rejected"
	}
	me.State, me.Note, me.At = verdict, note, now
	msg := fmt.Sprintf("%s task #%d", strings.ToUpper(verdict[:1])+verdict[1:], id)
	if note != "" {
		msg += ": " + note
	}
	if err := finishReviewTask(ctx, tx, project, me.ReviewTaskID, actor, msg, outcome, now); err != nil {
		return nil, err
	}
	v := &ReviewVerdict{Author: w.OriginalAuthor}
	orchAssignee := ""
	_ = tx.GetContext(ctx, &orchAssignee, "SELECT COALESCE(assigned_to,'') FROM board_tasks WHERE id=?", id)
	v.Orchestrator = orchAssignee
	if err := concludeRound(ctx, tx, project, id, &w, round, approve, v); err != nil {
		return nil, err
	}
	if err := saveWorkflow(ctx, tx, id, w); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if v.Task, err = s.GetTask(ctx, project, id); err != nil {
		return nil, err
	}
	v.ReviewTask, err = s.GetTask(ctx, project, me.ReviewTaskID)
	return v, err
}

// checkOpenReview: a verdict needs the task in review, with an open round. A task whose last round was rejected
// and is back with its author says so: the verdict waits for the resubmission, and the review task reopens then.
func checkOpenReview(id int64, status string, w TaskWorkflow) error {
	n := len(w.ReviewRounds)
	switch {
	case n > 0 && w.ReviewRounds[n-1].Result == "rejected" && status != "review_pending" && status != "completed":
		return fmt.Errorf("task #%d is back with %s (rejected in round %d): give your verdict when it is resubmitted, and your review task reopens", id, w.OriginalAuthor, w.ReviewRounds[n-1].Round)
	case status != "review_pending" || n == 0 || w.ReviewRounds[n-1].Result != "":
		return fmt.Errorf("task #%d has no open review", id)
	}
	return nil
}

// roundReviewer is actor's place among the round's reviewers. A verdict on an earlier round (reviewed, when not 0:
// the task was resubmitted since) is refused (#3787), and so is one from someone the task was not given to, or a
// second verdict in the round.
func roundReviewer(id int64, round *ReviewRound, actor string, reviewed int) (*ReviewerState, error) {
	if reviewed != 0 && reviewed != round.Round {
		return nil, fmt.Errorf("you reviewed round %d of task #%d, but it was resubmitted and is at round %d: read it again (coral-board task detail %d) and give your verdict with --round %d", reviewed, id, round.Round, id, round.Round)
	}
	var me *ReviewerState
	for i := range round.Reviewers {
		if round.Reviewers[i].Reviewer == actor {
			me = &round.Reviewers[i]
		}
	}
	if me == nil {
		return nil, fmt.Errorf("only the reviewers task #%d was given to can approve or reject it", id)
	}
	if me.State != "pending" {
		return nil, fmt.Errorf("you already %s task #%d in round %d", me.State, id, round.Round)
	}
	return me, nil
}

// checkReviewTaskOpen: the verdict goes on the reviewer's own open review task, claimed or not (#3787: any time the
// task is in review).
func checkReviewTaskOpen(ctx context.Context, tx *sqlx.Tx, project string, id, reviewTaskID int64, actor string) error {
	var rtStatus string
	var rtAssignee sql.NullString
	if err := tx.QueryRowxContext(ctx, "SELECT status, assigned_to FROM board_tasks WHERE id=? AND board_id=?", reviewTaskID, project).Scan(&rtStatus, &rtAssignee); err != nil {
		return err
	}
	if (rtStatus != "in_progress" && rtStatus != "pending") || rtAssignee.String != actor {
		return fmt.Errorf("your review task #%d for #%d is not open", reviewTaskID, id)
	}
	return nil
}

// concludeRound records what the verdict does to the round. A rejection ends it: the task goes back to its author,
// pending, with a fresh candidate to come, and the other reviewers stay on it (#3787: their review tasks stay open
// and reopen for the next round). An approval completes the round when every reviewer has approved: v.Complete,
// with the approvers in the order they were named.
func concludeRound(ctx context.Context, tx *sqlx.Tx, project string, id int64, w *TaskWorkflow, round *ReviewRound, approve bool, v *ReviewVerdict) error {
	if !approve {
		round.Result = "rejected"
		if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status='pending', assigned_to=?, claimed_at=NULL WHERE id=? AND board_id=?", ptr.NullIfEmpty(w.OriginalAuthor), id, project); err != nil {
			return err
		}
		w.CompletionReview = nil
		v.Rejected = true
		return nil
	}
	for _, r := range round.Reviewers {
		if r.State != "approved" {
			return nil
		}
	}
	round.Result = "approved"
	round.ConcludedAt = nowUTC()
	round.ApprovedDigests = patchDigests(w.CompletionReview)
	v.Complete = true
	for _, r := range round.Reviewers {
		v.Approvers = append(v.Approvers, r.Reviewer)
	}
	return nil
}

// finishReviewTask completes a review task (the reviewer's verdict), freeing their slot.
func finishReviewTask(ctx context.Context, tx *sqlx.Tx, project string, reviewTaskID int64, actor, msg, outcome, now string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status='completed', completed_by=?, completion_message=?, completed_at=? WHERE id=? AND board_id=? AND status IN ('pending','in_progress')",
		actor, msg, now, reviewTaskID, project); err != nil {
		return err
	}
	rw, err := loadWorkflow(ctx, tx, reviewTaskID)
	if err != nil {
		return err
	}
	rw.Outcome = outcome
	return saveWorkflow(ctx, tx, reviewTaskID, rw)
}

// lastRoundRejected: the task's latest review round ended in a rejection, so it is back with its
// author for a new candidate.
func lastRoundRejected(w TaskWorkflow) bool {
	n := len(w.ReviewRounds)
	return n > 0 && w.ReviewRounds[n-1].Result == "rejected"
}
