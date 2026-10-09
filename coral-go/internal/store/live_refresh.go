package store

import (
	"context"
	"strings"

	"github.com/jmoiron/sqlx"
)

// GetEditedFileCounts limits aggregation to the sessions being displayed.
func (s *TaskStore) GetEditedFileCounts(ctx context.Context, sessionIDs []string) (map[string]int, error) {
	result := make(map[string]int)
	if len(sessionIDs) == 0 {
		return result, nil
	}
	query, args, err := sqlx.In(`SELECT session_id, COUNT(DISTINCT json_extract(detail_json, '$.file_path')) AS cnt
		FROM agent_events WHERE session_id IN (?) AND event_type = 'tool_use'
		AND tool_name IN ('Write', 'Edit') AND detail_json IS NOT NULL GROUP BY session_id`, sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SessionID string `db:"session_id"`
		Count     int    `db:"cnt"`
	}
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(query), args...); err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.SessionID] = row.Count
	}
	return result, nil
}

// GetLiveGitState uses indexed latest-row lookups rather than grouping the
// entire snapshot history. Agent-name fallback is only needed when a live
// session has no snapshot of its own.
func (s *GitStore) GetLiveGitState(ctx context.Context, sessionAgents map[string]string) (map[string]*GitSnapshot, error) {
	result := make(map[string]*GitSnapshot)
	if len(sessionAgents) == 0 {
		return result, nil
	}
	values := make([]string, 0, len(sessionAgents))
	args := make([]any, 0, len(sessionAgents)*2)
	for id, name := range sessionAgents {
		values = append(values, "(?,?)")
		args = append(args, id, name)
	}
	query := `WITH wanted(session_id, agent_name) AS (VALUES ` + strings.Join(values, ",") + `)
		SELECT wanted.session_id AS target_session_id, g.id, g.agent_name, g.agent_type, g.working_directory, g.branch, g.commit_hash, g.commit_subject, g.commit_timestamp, g.session_id, g.remote_url, g.recorded_at, COALESCE(g.pr_number, 0) AS pr_number, COALESCE(g.is_worktree, 0) AS is_worktree, COALESCE(g.base_branch, '') AS base_branch, COALESCE(g.ahead, 0) AS ahead, COALESCE(g.behind, 0) AS behind, COALESCE(g.dirty_count, 0) AS dirty_count FROM wanted JOIN git_snapshots g ON g.id = COALESCE(
			(SELECT id FROM git_snapshots WHERE session_id = wanted.session_id ORDER BY recorded_at DESC, id DESC LIMIT 1),
			(SELECT id FROM git_snapshots WHERE agent_name = wanted.agent_name ORDER BY recorded_at DESC, id DESC LIMIT 1))`
	var rows []struct {
		TargetSessionID string `db:"target_session_id"`
		GitSnapshot
	}
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(query), args...); err != nil {
		return nil, err
	}
	for i := range rows {
		result[rows[i].TargetSessionID] = &rows[i].GitSnapshot
	}
	return result, nil
}
