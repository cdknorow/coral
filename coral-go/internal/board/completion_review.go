package board

import (
	"context"
	"fmt"
	"strings"
)

// CompletionReview preserves an unaccepted candidate independently of the final
// workflow result. Recording or releasing it never resolves dependencies.
type CompletionReview struct {
	SubmittedBy     string         `json:"submitted_by"`
	SubmittedAt     string         `json:"submitted_at"`
	Message         string         `json:"message"`
	ProposedOutcome string         `json:"proposed_outcome"`
	Artifacts       []TaskArtifact `json:"artifacts,omitempty"`
	Reason          string         `json:"reason"`
	ReleasedBy      string         `json:"released_by,omitempty"`
	ReleasedAt      string         `json:"released_at,omitempty"`
	ReleaseReason   string         `json:"release_reason,omitempty"`
}

// RequireTaskReviewer uses the board's registered privilege, never an arbitrary
// caller-supplied name containing "orchestrator". Like the existing local board
// API, callers identify themselves with subscriber_id.
func (s *Store) RequireTaskReviewer(ctx context.Context, project, actor string) error {
	sub, err := s.GetProjectSubscription(ctx, project, actor)
	if err != nil {
		return err
	}
	if sub == nil || sub.IsActive == 0 || (sub.CanPeek == 0 && !strings.EqualFold(strings.TrimSpace(sub.JobTitle), "Orchestrator")) {
		return fmt.Errorf("an active registered orchestrator is required")
	}
	return nil
}

// SubmitCompletionReview records evidence that actually reached Coral. It does
// not infer that a safety reviewer rejected an earlier command, and does not
// free the worker slot. The immutable candidate can be submitted before a
// completion attempt, or separately after an externally rejected attempt.
func (s *Store) SubmitCompletionReview(ctx context.Context, project string, id int64, actor, message, outcome, reason string, artifacts []TaskArtifact) (*Task, error) {
	return s.submitCompletionReviewAtRevision(ctx, project, id, actor, message, outcome, reason, artifacts, nil)
}

func (s *Store) SubmitCompletionReviewAtRevision(ctx context.Context, project string, id int64, actor, message, outcome, reason string, artifacts []TaskArtifact, expectedRevision *int) (*Task, error) {
	return s.submitCompletionReviewAtRevision(ctx, project, id, actor, message, outcome, reason, artifacts, expectedRevision)
}

func (s *Store) submitCompletionReviewAtRevision(ctx context.Context, project string, id int64, actor, message, outcome, reason string, artifacts []TaskArtifact, expectedRevision *int) (*Task, error) {
	if strings.TrimSpace(reason) == "" || len(reason) > 4096 || len(message) > 65536 {
		return nil, fmt.Errorf("a review reason is required (maximum 4096 characters); message maximum is 65536")
	}
	if outcome == "" {
		outcome = "success"
	}
	if outcome != "success" && outcome != "failed" {
		return nil, fmt.Errorf("proposed outcome must be success or failed")
	}
	if _, err := validateTaskArtifacts(artifacts); err != nil {
		return nil, err
	}
	reviewerErr := s.RequireTaskReviewer(ctx, project, actor)
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var row struct {
		Status     string  `db:"status"`
		AssignedTo *string `db:"assigned_to"`
		Revision   int     `db:"revision"`
	}
	if err := tx.GetContext(ctx, &row, "SELECT status,assigned_to,revision FROM board_tasks WHERE id=? AND board_id=?", id, project); err != nil {
		return nil, err
	}
	if row.Revision > 1 && (expectedRevision == nil || *expectedRevision != row.Revision) {
		return nil, fmt.Errorf("task #%d has revision %d; reread task detail before submitting review", id, row.Revision)
	}
	if row.Status != "in_progress" {
		return nil, fmt.Errorf("only an in-progress task can submit completion evidence")
	}
	if (row.AssignedTo == nil || *row.AssignedTo != actor) && reviewerErr != nil {
		return nil, reviewerErr
	}
	w, err := loadWorkflow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if w.CompletionReview != nil {
		return nil, fmt.Errorf("completion review evidence is already recorded and immutable")
	}
	w.CompletionReview = &CompletionReview{SubmittedBy: actor, SubmittedAt: nowUTC(), Message: message, ProposedOutcome: outcome, Reason: reason, Artifacts: artifacts}
	if err := saveWorkflow(ctx, tx, id, w); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, project, id)
}

// ReleaseCompletionReview releases capacity without accepting the candidate,
// recording a successful/failed outcome, or firing completion waits.
func (s *Store) ReleaseCompletionReview(ctx context.Context, project string, id int64, actor, reason string) (*Task, error) {
	if err := s.RequireTaskReviewer(ctx, project, actor); err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 4096 {
		return nil, fmt.Errorf("a release reason is required (maximum 4096 characters)")
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	w, err := loadWorkflow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if w.CompletionReview == nil {
		return nil, fmt.Errorf("submit candidate evidence before releasing the slot")
	}
	res, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status='review_pending' WHERE id=? AND board_id=? AND status='in_progress'", id, project)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, fmt.Errorf("task is not in progress on this board")
	}
	w.CompletionReview.ReleasedBy = actor
	w.CompletionReview.ReleasedAt = nowUTC()
	w.CompletionReview.ReleaseReason = reason
	if err := saveWorkflow(ctx, tx, id, w); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, project, id)
}
