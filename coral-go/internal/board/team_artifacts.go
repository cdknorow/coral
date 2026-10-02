package board

import (
	"context"
	"encoding/json"
)

// MaxTeamArtifactTaskScan bounds how many of a team's most recent
// workflow-bearing tasks are examined when listing artifacts.
const MaxTeamArtifactTaskScan = 500

// TeamArtifactTask is one team task's artifact references, as recorded in its
// workflow. No artifact bytes are loaded; inline content is only as large as
// the task already stored.
type TeamArtifactTask struct {
	TaskID      int64
	Title       string
	CreatedAt   string
	CompletedAt string
	Artifacts   []TaskArtifact
	Review      *CompletionReview
}

// ListTeamArtifactTasks returns up to limit of the project's most recent tasks
// that reference artifacts, newest task first. Tasks from other projects are
// never returned. truncated reports that older matching tasks exist.
func (s *Store) ListTeamArtifactTasks(ctx context.Context, project string, limit int) (tasks []TeamArtifactTask, truncated bool, err error) {
	if limit <= 0 || limit > MaxTeamArtifactTaskScan {
		limit = MaxTeamArtifactTaskScan
	}
	var rows []struct {
		ID          int64   `db:"id"`
		Title       string  `db:"title"`
		CreatedAt   string  `db:"created_at"`
		CompletedAt *string `db:"completed_at"`
		Data        string  `db:"data"`
	}
	// Prefilter in SQL so tasks without any artifacts are not decoded.
	err = s.db.SelectContext(ctx, &rows, `SELECT t.id, t.title, t.created_at, t.completed_at, w.data
		FROM board_tasks t JOIN task_workflows w ON w.task_id = t.id
		WHERE t.board_id = ? AND w.data LIKE '%"artifacts":[%'
		ORDER BY t.id DESC LIMIT ?`, project, limit+1)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		truncated = true
		rows = rows[:limit]
	}
	for _, row := range rows {
		var w TaskWorkflow
		if json.Unmarshal([]byte(row.Data), &w) != nil {
			continue
		}
		if len(w.Artifacts) == 0 && (w.CompletionReview == nil || len(w.CompletionReview.Artifacts) == 0) {
			continue
		}
		completed := ""
		if row.CompletedAt != nil {
			completed = *row.CompletedAt
		}
		tasks = append(tasks, TeamArtifactTask{TaskID: row.ID, Title: row.Title, CreatedAt: row.CreatedAt, CompletedAt: completed, Artifacts: w.Artifacts, Review: w.CompletionReview})
	}
	return tasks, truncated, nil
}
