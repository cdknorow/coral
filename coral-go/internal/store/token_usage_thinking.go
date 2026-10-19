package store

import (
	"context"
	"strings"
)

// ThinkingUsageSummary is token usage for one thinking level of one agent type (#4969). Thinking is "" for usage whose turn recorded no
// level (the CLI does not write one, or the call is older than the tracking): it is shown as "Not reported", never filled in.
// ThinkingTokens is the thinking tokens the CLIs reported for those calls (part of the output tokens for Codex; whether Claude counts
// them in its output tokens is not known, so the view shows them apart and never adds them to a total).
type ThinkingUsageSummary struct {
	AgentType        string  `db:"agent_type" json:"agent_type"`
	Thinking         string  `db:"thinking" json:"thinking"`
	InputTokens      int64   `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int64   `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int64   `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int64   `db:"cache_write_tokens" json:"cache_write_tokens"`
	TotalTokens      int64   `db:"total_tokens" json:"total_tokens"`
	ThinkingTokens   int64   `db:"thinking_tokens" json:"thinking_tokens"`
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`
	NumCalls         int64   `db:"n_calls" json:"num_calls"`
}

// recordThinking adds one stored call to its hour's line of the breakdown, when its turn recorded a level. Only the transcript-derived
// ("jsonl") calls count, as the Cost totals count them rather than the proxy's (the two would count the same call twice).
func (s *TokenUsageStore) recordThinking(ctx context.Context, u *TokenUsage) error {
	level := strings.TrimSpace(u.Thinking)
	if level == "" || u.Source != "jsonl" || len(u.RecordedAt) < 13 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO token_usage_thinking
		(hour, agent_type, thinking, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_tokens, thinking_tokens, cost_usd, n_calls)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(hour, agent_type, thinking) DO UPDATE SET
			input_tokens = input_tokens + excluded.input_tokens, output_tokens = output_tokens + excluded.output_tokens,
			cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens, cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
			total_tokens = total_tokens + excluded.total_tokens, thinking_tokens = thinking_tokens + excluded.thinking_tokens,
			cost_usd = cost_usd + excluded.cost_usd, n_calls = n_calls + 1`,
		u.RecordedAt[:13], u.AgentType, level, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.TotalTokens, u.ThinkingTokens, u.CostUSD)
	return err
}

// GetUsageByThinking returns the usage of calls that recorded a thinking level, by agent type and level, from the hour since falls in
// (the breakdown is kept by hour: a since inside an hour includes that whole hour). since "" is no lower bound.
func (s *TokenUsageStore) GetUsageByThinking(ctx context.Context, since string) ([]ThinkingUsageSummary, error) {
	out := []ThinkingUsageSummary{}
	q := `SELECT agent_type, thinking,
	        COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens,
	        COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens, COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
	        COALESCE(SUM(total_tokens), 0) AS total_tokens, COALESCE(SUM(thinking_tokens), 0) AS thinking_tokens,
	        COALESCE(SUM(cost_usd), 0) AS cost_usd, COALESCE(SUM(n_calls), 0) AS n_calls
	      FROM token_usage_thinking`
	var args []any
	if len(since) >= 13 {
		q += ` WHERE hour >= ?`
		args = append(args, since[:13])
	}
	q += ` GROUP BY agent_type, thinking ORDER BY cost_usd DESC, agent_type, thinking`
	err := s.db.Reader().SelectContext(ctx, &out, q, args...)
	return out, err
}
