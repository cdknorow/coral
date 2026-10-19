// What a task costs: the stored total and the live figure (moved out of store.go, #4998; no behaviour change).

package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// computeAndStoreTaskCost queries the sessions DB for proxy request costs
// incurred during the task's lifetime and stores them on the task.
func (s *Store) computeAndStoreTaskCost(ctx context.Context, project string, taskID int64) {
	if s.sessionsDB == nil {
		return
	}
	// Fetch the task's session_id and time window.
	var task struct {
		SessionID   *string `db:"session_id"`
		ClaimedAt   *string `db:"claimed_at"`
		CompletedAt *string `db:"completed_at"`
	}
	if err := s.reader().GetContext(ctx, &task,
		`SELECT session_id, claimed_at, completed_at FROM board_tasks WHERE id = ? AND board_id = ?`, taskID, project); err != nil {
		return
	}
	if task.SessionID == nil || task.ClaimedAt == nil || task.CompletedAt == nil {
		return
	}

	var costs struct {
		CostUSD          float64 `db:"cost_usd"`
		InputTokens      int     `db:"input_tokens"`
		OutputTokens     int     `db:"output_tokens"`
		CacheReadTokens  int     `db:"cache_read_tokens"`
		CacheWriteTokens int     `db:"cache_write_tokens"`
	}
	err := s.sessionsDB.GetContext(ctx, &costs,
		`SELECT COALESCE(SUM(cost_usd), 0) AS cost_usd,
		        COALESCE(SUM(input_tokens), 0) AS input_tokens,
		        COALESCE(SUM(output_tokens), 0) AS output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens
		 FROM token_usage_all
		 WHERE session_id = ? AND recorded_at >= ? AND recorded_at <= ?`,
		*task.SessionID, *task.ClaimedAt, *task.CompletedAt)
	if err != nil {
		return
	}

	s.db.ExecContext(ctx,
		`UPDATE board_tasks
		 SET cost_usd = ?, input_tokens = ?, output_tokens = ?,
		     cache_read_tokens = ?, cache_write_tokens = ?
		 WHERE id = ? AND board_id = ?`,
		costs.CostUSD, costs.InputTokens, costs.OutputTokens,
		costs.CacheReadTokens, costs.CacheWriteTokens, taskID, project)
}

// TaskLiveCost holds real-time cost data for an in-progress task.
type TaskLiveCost struct {
	TaskID           int64   `json:"task_id"`
	SessionID        string  `json:"session_id"`
	CostUSD          float64 `json:"cost_usd"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	RequestCount     int     `json:"request_count"`
}

// ErrTaskNotFound: no task with that id on that board (or no such board).
var ErrTaskNotFound = errors.New("task not found")

// GetTaskLiveCost computes the current cost for a task by querying
// token_usage from claimed_at to now. Returns nil if the task has no session_id
// or the sessions DB is not available, and ErrTaskNotFound for a missing task.
func (s *Store) GetTaskLiveCost(ctx context.Context, project string, taskID int64) (*TaskLiveCost, error) {
	if s.sessionsDB == nil {
		return nil, nil
	}

	var task struct {
		SessionID *string `db:"session_id"`
		ClaimedAt *string `db:"claimed_at"`
	}
	if err := s.reader().GetContext(ctx, &task,
		`SELECT session_id, claimed_at FROM board_tasks WHERE id = ? AND board_id = ?`,
		taskID, project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("task #%d: %w", taskID, err)
	}
	if task.SessionID == nil || task.ClaimedAt == nil {
		return nil, nil
	}

	var costs struct {
		CostUSD          float64 `db:"cost_usd"`
		InputTokens      int     `db:"input_tokens"`
		OutputTokens     int     `db:"output_tokens"`
		CacheReadTokens  int     `db:"cache_read_tokens"`
		CacheWriteTokens int     `db:"cache_write_tokens"`
		RequestCount     int     `db:"request_count"`
	}
	err := s.sessionsDB.GetContext(ctx, &costs,
		`SELECT COALESCE(SUM(cost_usd), 0) AS cost_usd,
		        COALESCE(SUM(input_tokens), 0) AS input_tokens,
		        COALESCE(SUM(output_tokens), 0) AS output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
		        COALESCE(SUM(n_rows), 0) AS request_count
		 FROM token_usage_all
		 WHERE session_id = ? AND recorded_at >= ?`,
		*task.SessionID, *task.ClaimedAt)
	if err != nil {
		return nil, err
	}

	return &TaskLiveCost{
		TaskID:           taskID,
		SessionID:        *task.SessionID,
		CostUSD:          costs.CostUSD,
		InputTokens:      costs.InputTokens,
		OutputTokens:     costs.OutputTokens,
		CacheReadTokens:  costs.CacheReadTokens,
		CacheWriteTokens: costs.CacheWriteTokens,
		RequestCount:     costs.RequestCount,
	}, nil
}
