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
		reasons = append(reasons, fmt.Sprintf("upstream is %s; requires %s", status, condition))
	} else if (condition == "success" && outcome != "success") || (condition == "failure" && outcome != "failed") {
		reasons = append(reasons, fmt.Sprintf("upstream outcome is %s; requires %s", outcome, condition))
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
