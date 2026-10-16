package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

// The review flow (#3725, settled with the operator):
//  1. submit-review gives the task to the team's orchestrator (status review_pending) and records
//     its original author; with no orchestrator on the team it completes the task.
//  2. The orchestrator completes it, or delegates it: one review task per reviewer, an ordinary
//     task in the reviewer's queue, claimed like any other (it takes their one slot).
//  3. Each reviewer approves or rejects it, on their claimed review task. When every reviewer of
//     the round has approved, the task completes itself. A rejection sends it back to its author
//     (pending, with the note) and ends the round's other reviews.

// ReviewRound is one delegation of a task to reviewers.
type ReviewRound struct {
	Round       int             `json:"round"`
	DelegatedBy string          `json:"delegated_by"`
	DelegatedAt string          `json:"delegated_at"`
	Reviewers   []ReviewerState `json:"reviewers"`
	Result      string          `json:"result,omitempty"` // "", approved or rejected
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
func (s *Store) HandToOrchestrator(ctx context.Context, project string, id int64, author string) (string, error) {
	orch, err := s.TeamOrchestrator(ctx, project)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	w, err := loadWorkflow(ctx, tx, id)
	if err != nil {
		return "", err
	}
	res, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status='review_pending', assigned_to=? WHERE id=? AND board_id=? AND status='in_progress'", orch, id, project)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", fmt.Errorf("task #%d is not in progress on this board", id)
	}
	w.OriginalAuthor = author
	if w.CompletionReview != nil {
		w.CompletionReview.ReleasedBy, w.CompletionReview.ReleasedAt, w.CompletionReview.ReleaseReason = "Coral", nowUTC(), "submitted for review: given to the orchestrator"
	}
	if err := saveWorkflow(ctx, tx, id, w); err != nil {
		return "", err
	}
	return orch, tx.Commit()
}

// DelegateReview assigns a task in review to reviewers (only the team's orchestrator, only a
// task in review): one review task each, in their queue. It refuses while a round is open, a
// reviewer who isn't an active subscriber, and the task's own author.
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
	if n := len(w.ReviewRounds); n > 0 && w.ReviewRounds[n-1].Result == "" {
		return nil, nil, fmt.Errorf("task #%d already has reviewers for round %d", id, w.ReviewRounds[n-1].Round)
	}
	for _, r := range clean {
		if strings.EqualFold(r, w.OriginalAuthor) {
			return nil, nil, fmt.Errorf("%s did the work and can't review it", r)
		}
		sub, err := s.GetProjectSubscription(ctx, project, r)
		if err != nil {
			return nil, nil, err
		}
		if sub == nil || sub.IsActive == 0 {
			return nil, nil, fmt.Errorf("%s is not on this board", r)
		}
	}
	round := ReviewRound{Round: len(w.ReviewRounds) + 1, DelegatedBy: actor, DelegatedAt: nowUTC()}
	var created []*Task
	for _, r := range clean {
		body := fmt.Sprintf("Review task #%d, %q, for %s (round %d).\nRead it: coral-board task detail %d\nThen: coral-board task approve %d [--note \"...\"], or coral-board task reject %d --note \"why\".",
			id, task.Title, w.OriginalAuthor, round.Round, id, id, id)
		rt, err := s.CreateTaskWithOpts(ctx, project, fmt.Sprintf("Review #%d: %s", id, task.Title), body, task.Priority, actor,
			&CreateTaskOpts{Workflow: TaskWorkflow{ReviewOf: id}}, r)
		if err != nil {
			return nil, nil, err
		}
		created = append(created, rt)
		round.Reviewers = append(round.Reviewers, ReviewerState{Reviewer: r, ReviewTaskID: rt.ID, State: "pending"})
	}
	w.ReviewRounds = append(w.ReviewRounds, round)
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
}

// ApproveReview records actor's approval, on the review task actor has claimed (id may name the
// task under review or the review task).
func (s *Store) ApproveReview(ctx context.Context, project string, id int64, actor, note string) (*ReviewVerdict, error) {
	return s.reviewVerdict(ctx, project, id, actor, note, true)
}

// RejectReview records actor's rejection (a note is required): the task goes back to its author.
func (s *Store) RejectReview(ctx context.Context, project string, id int64, actor, note string) (*ReviewVerdict, error) {
	if strings.TrimSpace(note) == "" {
		return nil, fmt.Errorf("say why: a rejection needs --note")
	}
	return s.reviewVerdict(ctx, project, id, actor, note, false)
}

func (s *Store) reviewVerdict(ctx context.Context, project string, id int64, actor, note string, approve bool) (*ReviewVerdict, error) {
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
	n := len(w.ReviewRounds)
	if status != "review_pending" || n == 0 || w.ReviewRounds[n-1].Result != "" {
		return nil, fmt.Errorf("task #%d has no open review", id)
	}
	round := &w.ReviewRounds[n-1]
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
	// The review task is an ordinary task: the reviewer claims it first (it takes their slot).
	var rtStatus string
	var rtAssignee sql.NullString
	if err := tx.QueryRowxContext(ctx, "SELECT status, assigned_to FROM board_tasks WHERE id=? AND board_id=?", me.ReviewTaskID, project).Scan(&rtStatus, &rtAssignee); err != nil {
		return nil, err
	}
	if rtStatus != "in_progress" || rtAssignee.String != actor {
		return nil, fmt.Errorf("claim your review task #%d first (coral-board task claim %d)", me.ReviewTaskID, me.ReviewTaskID)
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
	if !approve {
		round.Result = "rejected"
		for _, r := range round.Reviewers {
			if r.State == "pending" { // the round is over: their reviews end unanswered
				if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status='skipped', completed_by=?, completion_message=?, completed_at=? WHERE id=? AND board_id=? AND status IN ('pending','in_progress')",
					actor, fmt.Sprintf("Task #%d was rejected by %s", id, actor), now, r.ReviewTaskID, project); err != nil {
					return nil, err
				}
			}
		}
		// Back to its author, pending, with a fresh candidate to come.
		if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status='pending', assigned_to=?, claimed_at=NULL WHERE id=? AND board_id=?", nullIfEmpty(w.OriginalAuthor), id, project); err != nil {
			return nil, err
		}
		w.CompletionReview = nil
		v.Rejected = true
	} else {
		all := true
		for _, r := range round.Reviewers {
			all = all && r.State == "approved"
		}
		if all {
			round.Result = "approved"
			v.Complete = true
		}
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

// finishReviewTask completes a review task (the reviewer's verdict), freeing their slot.
func finishReviewTask(ctx context.Context, tx *sqlx.Tx, project string, reviewTaskID int64, actor, msg, outcome, now string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status='completed', completed_by=?, completion_message=?, completed_at=? WHERE id=? AND board_id=? AND status='in_progress'",
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

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
