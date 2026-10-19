package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// batchCalls are calls of a few sessions, with levels and sources mixed, a repeat of an earlier call inside
// the batch, and two reports with no timestamp that land on the same instant.
func batchCalls(n int) []*TokenUsage {
	var out []*TokenUsage
	levels := []string{"", "high", "low", "medium"}
	for i := 0; i < n; i++ {
		src := "jsonl"
		if i%7 == 3 {
			src = "proxy"
		}
		out = append(out, &TokenUsage{
			SessionID: fmt.Sprintf("s%d", i%5), AgentName: "a", AgentType: []string{"claude", "codex"}[i%2], Model: []string{"m1", "", "m2"}[i%3],
			Thinking: levels[i%4], ThinkingTokens: i % 9, Source: src,
			RecordedAt:  fmt.Sprintf("2026-10-01T%02d:%02d:00Z", 10+i/60%10, i%60),
			InputTokens: 100 + i, OutputTokens: 10 + i, CacheReadTokens: i, TotalTokens: 110 + 3*i, CostUSD: float64(i) / 10,
		})
	}
	// the same call twice (the second is ignored), and two with no timestamp at one instant (the second takes the next microsecond)
	if len(out) > 0 {
		out = append(out, &TokenUsage{SessionID: "s0", AgentName: "a", RecordedAt: out[0].RecordedAt, InputTokens: 1, TotalTokens: 1, Thinking: "high", Source: "jsonl"})
	}
	out = append(out, &TokenUsage{SessionID: "stamp", AgentName: "a", InputTokens: 5, TotalTokens: 5, Thinking: "low", Source: "jsonl"})
	out = append(out, &TokenUsage{SessionID: "stamp", AgentName: "a", InputTokens: 6, TotalTokens: 6, Thinking: "low", Source: "jsonl"})
	return out
}

func copyCalls(in []*TokenUsage) []*TokenUsage {
	out := make([]*TokenUsage, len(in))
	for i, u := range in {
		c := *u
		out[i] = &c
	}
	return out
}

func usageState(t *testing.T, s *TokenUsageStore) ([]TokenUsage, []ThinkingUsageSummary) {
	t.Helper()
	var rows []TokenUsage
	require.NoError(t, s.db.Reader().SelectContext(context.Background(), &rows, `SELECT id, session_id, agent_name, agent_type, team_id, board_name, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_tokens, cost_usd, num_turns, session_start_at, last_activity_at, recorded_at, source, COALESCE(model, '') AS model FROM token_usage ORDER BY id`))
	th, err := s.GetUsageByThinking(context.Background(), "")
	require.NoError(t, err)
	return rows, th
}

// The oracle: RecordUsageBatch leaves the database as RecordUsage one call at a time does: the same rows with the same ids (so the
// same duplicates ignored and the same free microsecond for a report with no timestamp), the same thinking breakdown, and the
// same ID set on each record. Over a batch that is one transaction and one that is several (usageBatchMax records a transaction).
func TestRecordUsageBatchIsRecordUsageOneAtATime(t *testing.T) {
	old := tokenUsageNow
	tokenUsageNow = func() string { return "2026-10-16T12:00:00.000000+00:00" }
	defer func() { tokenUsageNow = old }()
	for _, n := range []int{0, 1, 50, usageBatchMax, usageBatchMax + 1, 2*usageBatchMax + 37} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			ctx := context.Background()
			calls := batchCalls(n)
			one := NewTokenUsageStore(openTestDB(t))
			oneCalls := copyCalls(calls)
			for _, u := range oneCalls {
				require.NoError(t, one.RecordUsage(ctx, u))
			}
			batch := NewTokenUsageStore(openTestDB(t))
			inBatch := copyCalls(calls)
			for i, err := range batch.RecordUsageBatch(ctx, inBatch) {
				require.NoError(t, err, "record %d", i)
			}
			wantRows, wantThinking := usageState(t, one)
			gotRows, gotThinking := usageState(t, batch)
			assert.Equal(t, wantRows, gotRows)
			assert.Equal(t, wantThinking, gotThinking)
			for i := range oneCalls {
				assert.Equal(t, oneCalls[i].ID, inBatch[i].ID, "record %d", i)
				assert.Equal(t, oneCalls[i].RecordedAt, inBatch[i].RecordedAt, "record %d", i)
			}
			if n >= 50 {
				assert.NotEmpty(t, gotThinking, "the breakdown is not vacuous")
				assert.Greater(t, len(gotRows), n/2)
			}
		})
	}
}

// A record that fails does not stop the others, and what it did not store leaves no thinking line behind.
func TestRecordUsageBatchOneFailureLeavesTheOthers(t *testing.T) {
	ctx := context.Background()
	s := NewTokenUsageStore(openTestDB(t))
	_, err := s.db.ExecContext(ctx, `CREATE TRIGGER token_usage_refuse BEFORE INSERT ON token_usage WHEN NEW.session_id = 'bad' BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	require.NoError(t, err)
	calls := []*TokenUsage{
		{SessionID: "ok1", AgentName: "a", RecordedAt: "2026-10-01T10:00:00Z", InputTokens: 1, TotalTokens: 1, Thinking: "high", Source: "jsonl"},
		{SessionID: "bad", AgentName: "a", RecordedAt: "2026-10-01T10:01:00Z", InputTokens: 2, TotalTokens: 2, Thinking: "high", Source: "jsonl"},
		{SessionID: "ok2", AgentName: "a", RecordedAt: "2026-10-01T10:02:00Z", InputTokens: 4, TotalTokens: 4, Thinking: "high", Source: "jsonl"},
	}
	errs := s.RecordUsageBatch(ctx, calls)
	require.Len(t, errs, 3)
	assert.NoError(t, errs[0])
	assert.Error(t, errs[1])
	assert.NoError(t, errs[2])
	rows, th := usageState(t, s)
	require.Len(t, rows, 2)
	assert.Equal(t, "ok1", rows[0].SessionID)
	assert.Equal(t, "ok2", rows[1].SessionID)
	require.Len(t, th, 1)
	assert.EqualValues(t, 2, th[0].NumCalls, "only the stored calls are in the breakdown")
	assert.EqualValues(t, 5, th[0].InputTokens)
}

// A batch prepares each of its statements once: the two statements of a record are the same for every record.
func TestPreparedExecerPreparesEachQueryOnce(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tx, err := db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	ex := &preparedExecer{tx: tx}
	const ins = `INSERT INTO token_usage (session_id, agent_name, recorded_at) VALUES (?, 'a', ?)`
	const del = `DELETE FROM token_usage WHERE recorded_at = ?`
	var first map[string]*sql.Stmt
	for i := 0; i < 5; i++ {
		_, err := ex.ExecContext(ctx, ins, "s", fmt.Sprint("t", i))
		require.NoError(t, err)
		_, err = ex.ExecContext(ctx, del, fmt.Sprint("t", i))
		require.NoError(t, err)
		if first == nil {
			first = map[string]*sql.Stmt{ins: ex.stmts[ins], del: ex.stmts[del]}
		}
		assert.Same(t, first[ins], ex.stmts[ins], "the statement prepared the first time is the one used again")
		assert.Same(t, first[del], ex.stmts[del])
	}
	assert.Len(t, ex.stmts, 2)
	ex.close()
}

// A cancelled context writes nothing and says so for every record.
func TestRecordUsageBatchCancelledWritesNothing(t *testing.T) {
	s := NewTokenUsageStore(openTestDB(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	errs := s.RecordUsageBatch(ctx, batchCalls(10))
	for i, err := range errs {
		assert.Error(t, err, "record %d", i)
	}
	rows, _ := usageState(t, s)
	assert.Empty(t, rows)
}

// The token poller's first read of a long transcript: 2000 calls of one session.
func benchRecordCalls(b *testing.B, record func(*TokenUsageStore, []*TokenUsage)) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db, err := OpenWithKey(context.Background(), filepath.Join(b.TempDir(), "bench.db"), "")
		require.NoError(b, err)
		s := NewTokenUsageStore(db)
		calls := make([]*TokenUsage, 2000)
		for j := range calls {
			calls[j] = &TokenUsage{SessionID: "s", AgentName: "a", Model: "claude-opus-5-5", Thinking: "high", Source: "jsonl",
				RecordedAt:  fmt.Sprintf("2026-10-01T10:%02d:%02d.%03dZ", j/3600, j/60%60, j%60),
				InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CostUSD: 0.01}
		}
		b.StartTimer()
		record(s, calls)
		b.StopTimer()
		db.Close()
	}
}

func BenchmarkRecordUsageOneAtATime(b *testing.B) {
	benchRecordCalls(b, func(s *TokenUsageStore, calls []*TokenUsage) {
		for _, u := range calls {
			if err := s.RecordUsage(context.Background(), u); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkRecordUsageBatch(b *testing.B) {
	benchRecordCalls(b, func(s *TokenUsageStore, calls []*TokenUsage) {
		for _, err := range s.RecordUsageBatch(context.Background(), calls) {
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
