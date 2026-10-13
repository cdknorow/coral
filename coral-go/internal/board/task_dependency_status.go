package board

import (
	"context"
	"fmt"
	"strings"
)

// dependencyStatus is shared by claim readiness and public diagnostics so that
// 'completed' never hides an unmet outcome or required artifact.
func dependencyStatus(status string, w TaskWorkflow, condition string, required []string) (string, []string, string) {
	outcome := w.Outcome
	if status == "completed" && outcome == "" {
		outcome = "success"
	}
	if status == "skipped" {
		outcome = "cancelled"
	}
	if condition == "" {
		condition = "success"
	}
	var missing []string
	for _, name := range required {
		found := false
		for _, a := range w.Artifacts {
			if a.Name == name {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}
	var reasons []string
	if status != "completed" && status != "skipped" {
		reasons = append(reasons, fmt.Sprintf("not finished yet (%s); this task starts when it %s", plainTaskStatus(status), plainCondition(condition)))
	} else if (condition == "success" && outcome != "success") || (condition == "failure" && outcome != "failed") {
		reasons = append(reasons, fmt.Sprintf("it %s, but this task needs it to %s", plainOutcome(outcome), plainConditionVerb(condition)))
	}
	if len(missing) > 0 {
		reasons = append(reasons, "missing required artifacts: "+strings.Join(missing, ", "))
	}
	return outcome, missing, strings.Join(reasons, "; ")
}

type TaskBlockage struct {
	ID      int64    `json:"id"`
	Title   string   `json:"title"`
	Reasons []string `json:"reasons"`
}

func DescribeTaskBlockage(id int64, title string, deps []TaskDep) TaskBlockage {
	b := TaskBlockage{ID: id, Title: title, Reasons: []string{}}
	for _, d := range deps {
		if !d.Satisfied {
			b.Reasons = append(b.Reasons, fmt.Sprintf("#%d: %s", d.TaskID, d.BlockedReason))
		}
	}
	if len(b.Reasons) == 0 {
		b.Reasons = append(b.Reasons, "dependency rules are satisfied but task status is blocked; readiness metadata needs reconciliation")
	}
	return b
}

// BlockedTasksForSubscriber returns a bounded explanation for this worker's
// otherwise eligible blocked tasks. This is diagnostic only, never a claim.
func (s *Store) BlockedTasksForSubscriber(ctx context.Context, project, subscriber string, requestedID int64) ([]TaskBlockage, error) {
	var rows []Task
	err := s.db.SelectContext(ctx, &rows, `SELECT id,title FROM board_tasks WHERE board_id=? AND status='blocked' AND (assigned_to=? OR assigned_to IS NULL OR assigned_to='') AND (?=0 OR id=?) ORDER BY id LIMIT 20`, project, subscriber, requestedID, requestedID)
	if err != nil {
		return nil, err
	}
	result := []TaskBlockage{}
	for _, t := range rows {
		deps, err := s.GetTaskDependencies(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, DescribeTaskBlockage(t.ID, t.Title, deps))
	}
	return result, nil
}

// plainTaskStatus names a task status the way a person would say it.
func plainTaskStatus(status string) string {
	switch status {
	case "pending":
		return "not started"
	case "in_progress":
		return "in progress"
	case "review_pending":
		return "in review"
	case "blocked":
		return "itself blocked"
	case "":
		return "unknown"
	}
	return strings.ReplaceAll(status, "_", " ")
}

// plainCondition is what a dependency waits for, as a verb: "succeeds", "fails", "finishes".
func plainCondition(condition string) string {
	switch condition {
	case "failure":
		return "fails"
	case "termination":
		return "finishes"
	}
	return "succeeds"
}

// plainConditionVerb is plainCondition's infinitive: "succeed", "fail", "finish".
func plainConditionVerb(condition string) string {
	switch condition {
	case "failure":
		return "fail"
	case "termination":
		return "finish"
	}
	return "succeed"
}

// plainOutcome describes how a finished upstream task ended.
func plainOutcome(outcome string) string {
	switch outcome {
	case "success":
		return "succeeded"
	case "failed":
		return "failed"
	case "cancelled":
		return "was cancelled"
	}
	return "ended (" + strings.ReplaceAll(outcome, "_", " ") + ")"
}
