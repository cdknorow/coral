package store

import (
	"context"
	"time"
)

type CallMetric struct {
	CallType   string `json:"call_type"`
	Operation  string `json:"operation"`
	AgentName  string `json:"agent_name,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	TeamID     *int64 `json:"team_id,omitempty"`
	BoardName  string `json:"board_name,omitempty"`
	Method     string `json:"method,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	IsError    bool   `json:"is_error"`
	CreatedAt  string `json:"created_at"`
}

func (s *DB) RecordCallMetric(ctx context.Context, m *CallMetric) error {
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.ExecContext(ctx, `INSERT INTO call_metrics(call_type,operation,agent_name,session_id,team_id,board_name,method,status_code,duration_ms,is_error,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, m.CallType, m.Operation, m.AgentName, m.SessionID, m.TeamID, m.BoardName, m.Method, m.StatusCode, m.DurationMs, m.IsError, m.CreatedAt)
	return err
}

type CallMetricSummary struct {
	Operation     string  `db:"operation" json:"operation"`
	CallType      string  `db:"call_type" json:"call_type"`
	AgentName     string  `db:"agent_name" json:"agent_name"`
	BoardName     string  `db:"board_name" json:"board_name"`
	Calls         int     `db:"calls" json:"calls"`
	Errors        int     `db:"errors" json:"errors"`
	AvgDurationMs float64 `db:"avg_duration_ms" json:"avg_duration_ms"`
}

func (s *DB) CallMetricSummary(ctx context.Context, since time.Time) ([]CallMetricSummary, error) {
	var out []CallMetricSummary
	err := s.SelectContext(ctx, &out, `SELECT cm.operation, cm.call_type,
		COALESCE(NULLIF(ls.display_name, ''), cm.agent_name, '') AS agent_name,
		COALESCE(NULLIF(cm.board_name, ''), ls.board_name, '') AS board_name,
		COUNT(*) AS calls,
		SUM(cm.is_error) AS errors,
		COALESCE(AVG(cm.duration_ms), 0) AS avg_duration_ms
		FROM call_metrics cm LEFT JOIN live_sessions ls ON ls.session_id = cm.session_id
		WHERE cm.created_at >= ?
		GROUP BY cm.operation, cm.call_type,
		COALESCE(NULLIF(ls.display_name, ''), cm.agent_name, ''),
		COALESCE(NULLIF(cm.board_name, ''), ls.board_name, '')
		ORDER BY COUNT(*) DESC`, since.UTC().Format(time.RFC3339Nano))
	return out, err
}
