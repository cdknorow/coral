package board

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cdknorow/coral/internal/naming"
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
	// Stored ownership used for attribution: the subscriber who claimed the
	// task (AssignedTo), the one who completed it (CompletedBy), and the
	// Coral session recorded when the task was claimed (SessionID).
	AssignedTo  string
	CompletedBy string
	SessionID   string
}

// TeamSubscriberSessions maps each of the project's subscriber ids to the Coral
// session UUID derived from its stored session name. Subscribers without a
// session name are omitted. One row per subscriber, so the result is bounded
// by team size.
func (s *Store) TeamSubscriberSessions(ctx context.Context, project string) (map[string]string, error) {
	var rows []struct {
		SubscriberID string  `db:"subscriber_id"`
		SessionName  *string `db:"session_name"`
	}
	if err := s.db.SelectContext(ctx, &rows, `SELECT subscriber_id, session_name FROM board_subscribers WHERE project = ? AND subscriber_id IS NOT NULL`, project); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.SessionName == nil || strings.TrimSpace(*r.SessionName) == "" || strings.TrimSpace(r.SubscriberID) == "" {
			continue
		}
		out[r.SubscriberID] = naming.SessionIDFromName(*r.SessionName)
	}
	return out, nil
}

// ArtifactProducer returns the subscriber that produced an artifact reference
// and the Coral session it is attributed to. Attribution uses stored
// ownership only: completion artifacts belong to the task's completed_by
// subscriber and review artifacts to completion_review.submitted_by. The
// session is the task's recorded claim session when the producer is the
// claimant, otherwise the subscriber's stored session. Either value is empty
// when it cannot be established; callers must not guess.
func ArtifactProducer(t TeamArtifactTask, source string, subs map[string]string) (producer, session string) {
	switch source {
	case "completion":
		producer = t.CompletedBy
	case "review":
		if t.Review != nil {
			producer = t.Review.SubmittedBy
		}
	}
	if producer == "" {
		return "", ""
	}
	if t.SessionID != "" && producer == t.AssignedTo {
		return producer, t.SessionID
	}
	return producer, subs[producer]
}

// TeamArtifactQuery bounds a team artifact scan. SessionID, when set, narrows
// the scan itself (before the limit) to tasks the session's subscribers
// could have produced; exact attribution is then checked per artifact by
// ArtifactProducer. Subscribers is the TeamSubscriberSessions map.
type TeamArtifactQuery struct {
	Project     string
	Limit       int
	SessionID   string
	Subscribers map[string]string
}

// ListTeamArtifactTasks returns up to limit of the project's most recent tasks
// that reference artifacts, newest task first. Tasks from other projects are
// never returned. truncated reports that older matching tasks exist.
func (s *Store) ListTeamArtifactTasks(ctx context.Context, project string, limit int) (tasks []TeamArtifactTask, truncated bool, err error) {
	return s.ListTeamArtifactTasksQuery(ctx, TeamArtifactQuery{Project: project, Limit: limit})
}

func likeEscape(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}

// ListTeamArtifactTasksQuery is ListTeamArtifactTasks with an optional session
// filter applied inside the bounded SQL scan.
func (s *Store) ListTeamArtifactTasksQuery(ctx context.Context, q TeamArtifactQuery) (tasks []TeamArtifactTask, truncated bool, err error) {
	limit, project := q.Limit, q.Project
	if limit <= 0 || limit > MaxTeamArtifactTaskScan {
		limit = MaxTeamArtifactTaskScan
	}
	var rows []struct {
		ID          int64   `db:"id"`
		Title       string  `db:"title"`
		CreatedAt   string  `db:"created_at"`
		CompletedAt *string `db:"completed_at"`
		AssignedTo  *string `db:"assigned_to"`
		CompletedBy *string `db:"completed_by"`
		SessionID   *string `db:"session_id"`
		Data        string  `db:"data"`
	}
	// Prefilter in SQL so tasks without any artifacts are not decoded.
	query := `SELECT t.id, t.title, t.created_at, t.completed_at, t.assigned_to, t.completed_by, t.session_id, w.data
		FROM board_tasks t JOIN task_workflows w ON w.task_id = t.id
		WHERE t.board_id = ? AND w.data LIKE '%"artifacts":[%'`
	args := []any{project}
	if q.SessionID != "" {
		// Only tasks this session could own: recorded at claim, completed by
		// one of its subscribers, or holding a review submitted by one.
		preds := []string{"t.session_id = ?"}
		args = append(args, q.SessionID)
		for id, session := range q.Subscribers {
			if session != q.SessionID {
				continue
			}
			preds = append(preds, "t.completed_by = ?", `w.data LIKE ? ESCAPE '\'`)
			quoted, _ := json.Marshal(id)
			args = append(args, id, `%"submitted_by":`+likeEscape(string(quoted))+`%`)
		}
		query += " AND (" + strings.Join(preds, " OR ") + ")"
	}
	query += " ORDER BY t.id DESC LIMIT ?"
	args = append(args, limit+1)
	err = s.db.SelectContext(ctx, &rows, query, args...)
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
		t := TeamArtifactTask{TaskID: row.ID, Title: row.Title, CreatedAt: row.CreatedAt, CompletedAt: completed, Artifacts: w.Artifacts, Review: w.CompletionReview}
		if row.AssignedTo != nil {
			t.AssignedTo = *row.AssignedTo
		}
		if row.CompletedBy != nil {
			t.CompletedBy = *row.CompletedBy
		}
		if row.SessionID != nil {
			t.SessionID = *row.SessionID
		}
		tasks = append(tasks, t)
	}
	return tasks, truncated, nil
}
