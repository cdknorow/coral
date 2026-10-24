package board

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

// CompletionReview preserves an unaccepted candidate independently of the final
// workflow result. Recording or releasing it never resolves dependencies.
type CompletionReview struct {
	SubmittedBy       string                 `json:"submitted_by"`
	SubmittedAt       string                 `json:"submitted_at"`
	Message           string                 `json:"message"`
	ProposedOutcome   string                 `json:"proposed_outcome"`
	Artifacts         []TaskArtifact         `json:"artifacts,omitempty"`
	Reason            string                 `json:"reason"`
	CandidateRevision string                 `json:"candidate_revision,omitempty"`
	GateResults       []CompletionGateResult `json:"gate_results,omitempty"`
	ReleasedBy        string                 `json:"released_by,omitempty"`
	ReleasedAt        string                 `json:"released_at,omitempty"`
	ReleaseReason     string                 `json:"release_reason,omitempty"`
}

// RequireTaskReviewer uses the board's registered privilege, never an arbitrary
// caller-supplied name containing "orchestrator". Like the existing local board
// API, callers identify themselves with subscriber_id.
func (s *Store) RequireTaskReviewer(ctx context.Context, project, actor string) error {
	sub, err := s.GetProjectSubscription(ctx, project, actor)
	if err != nil {
		return err
	}
	// GetProjectSubscription selects only is_active = 1 rows, so IsActive == 0 cannot be true here today: the check mirrors that SQL filter
	// and keeps the rule if the lookup ever returns inactive rows.
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
	return s.SubmitCompletionReviewAtRevisionAndCandidate(ctx, project, id, actor, message, outcome, reason, artifacts, nil, "")
}

// SubmitCompletionReviewAtRevisionAndCandidate is SubmitCompletionReview against an expected
// task revision (nil: any) and a submitted candidate revision ("" for none).
func (s *Store) SubmitCompletionReviewAtRevisionAndCandidate(ctx context.Context, project string, id int64, actor, message, outcome, reason string, artifacts []TaskArtifact, expectedRevision *int, candidateRevision string) (*Task, error) {
	if strings.TrimSpace(reason) == "" || len(reason) > 4096 || len(message) > 65536 {
		return nil, fmt.Errorf("a review reason is required (maximum 4096 characters); message maximum is 65536")
	}
	if outcome == "" {
		outcome = "success"
	}
	if outcome != "success" && outcome != "failed" {
		return nil, fmt.Errorf("proposed outcome must be success or failed")
	}
	artifacts = copyArtifacts(artifacts) // the caller's rows are never written to (data race under concurrent submits of one slice)
	if _, err := validateTaskArtifacts(artifacts); err != nil {
		return nil, err
	}
	clearStamps(artifacts) // the server's fields: only attach sets them (#5689)
	reviewerErr := s.RequireTaskReviewer(ctx, project, actor)
	// Run twice: before the gates (read transaction, closed before slow
	// registered checks run) and inside the write transaction. See
	// completion_snapshot.go.
	validate := func(q sqlx.QueryerContext) (TaskWorkflow, error) {
		var row struct {
			Status     string  `db:"status"`
			AssignedTo *string `db:"assigned_to"`
			Revision   int     `db:"revision"`
		}
		if err := sqlx.GetContext(ctx, q, &row, "SELECT status,assigned_to,revision FROM board_tasks WHERE id=? AND board_id=?", id, project); err != nil {
			return TaskWorkflow{}, err
		}
		if row.Revision > 1 && (expectedRevision == nil || *expectedRevision != row.Revision) {
			return TaskWorkflow{}, fmt.Errorf("task #%d has revision %d; reread task detail before submitting review", id, row.Revision)
		}
		w, err := loadWorkflow(ctx, q, id)
		if err != nil {
			return w, err
		}
		// A task a reviewer rejected is back with its author, pending: the author resubmits it
		// without claiming it again, so another task in progress doesn't stand in the way (#3889).
		resubmit := row.Status == "pending" && row.AssignedTo != nil && *row.AssignedTo == actor && lastRoundRejected(w)
		if row.Status != "in_progress" && !resubmit {
			return TaskWorkflow{}, fmt.Errorf("only an in-progress task can submit completion evidence")
		}
		if (row.AssignedTo == nil || *row.AssignedTo != actor) && reviewerErr != nil {
			return TaskWorkflow{}, reviewerErr
		}
		if w.CompletionReview != nil {
			return w, fmt.Errorf("completion review evidence is already recorded and immutable")
		}
		return w, nil
	}
	w, snapshot, err := readForGates(ctx, s.db, project, id, validate)
	if err != nil {
		return nil, err
	}
	gateResults, gateErr := checkCompletionGatesWithPolicy(w.CompletionGates, artifacts, candidateRevision, s.completionCheckRunner, s.completionChecksEnabled)
	if gateErr != nil && outcome == "success" {
		return nil, gateErr
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if w, err = validate(tx); err != nil {
		return nil, err
	}
	if err := requireUnchanged(ctx, tx, project, id, snapshot); err != nil {
		return nil, err
	}
	w.CompletionReview = &CompletionReview{SubmittedBy: actor, SubmittedAt: nowUTC(), Message: message, ProposedOutcome: outcome, Reason: reason, Artifacts: artifacts, CandidateRevision: candidateRevision, GateResults: gateResults}
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
	res, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status='review_pending', edited_at=? WHERE id=? AND board_id=? AND status='in_progress'", nowUTC(), id, project)
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
