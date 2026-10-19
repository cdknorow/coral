package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5120: the breakdown is kept by model too: the same level of two models is two rows, a call that names no model is under the empty model.
func TestUsageByThinkingIsKeptByModel(t *testing.T) {
	s := NewTokenUsageStore(openTestDB(t))
	ctx := context.Background()
	rec := func(sid, model, level, at string, in int) {
		t.Helper()
		require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: sid, AgentName: sid, AgentType: "claude", Model: model, Thinking: level, Source: "jsonl",
			RecordedAt: at, InputTokens: in, TotalTokens: in, CostUSD: float64(in) / 100}))
	}
	rec("s1", "claude-opus-5-5", "high", "2026-10-19T10:00:00Z", 100)
	rec("s1", "claude-opus-5-5", "high", "2026-10-19T10:30:00Z", 50)
	rec("s2", "claude-sonnet-5-5", "high", "2026-10-19T10:10:00Z", 20)
	rec("s3", "claude-opus-5-5", "low", "2026-10-19T11:00:00Z", 7)
	rec("s4", "  claude-opus-5-5  ", "low", "2026-10-19T11:05:00Z", 3) // the model is trimmed, as the usage row's is
	rec("s5", "", "high", "2026-10-19T11:10:00Z", 5)

	rows, err := s.GetUsageByThinking(ctx, "")
	require.NoError(t, err)
	got := map[string]ThinkingUsageSummary{}
	for _, r := range rows {
		got[r.Model+"|"+r.Thinking] = r
	}
	require.Len(t, got, 4, "%+v", rows)
	assert.EqualValues(t, 150, got["claude-opus-5-5|high"].InputTokens)
	assert.EqualValues(t, 2, got["claude-opus-5-5|high"].NumCalls)
	assert.EqualValues(t, 20, got["claude-sonnet-5-5|high"].InputTokens, "the same level of another model is its own row")
	assert.EqualValues(t, 10, got["claude-opus-5-5|low"].InputTokens, "trimmed, so it is the same model")
	assert.EqualValues(t, 5, got["|high"].InputTokens, "a call that named no model is under the empty model")
}

// A table made before the model was in its key is rebuilt with every row kept under the empty model, and the key then holds the model: a second
// level of the same hour and type, a second model, is a row of its own.
func TestThinkingTableWithoutAModelIsMigratedKeepingEveryRow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	for _, q := range []string{
		`DROP TABLE token_usage_thinking`,
		`CREATE TABLE token_usage_thinking (hour TEXT NOT NULL, agent_type TEXT NOT NULL, thinking TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_tokens INTEGER NOT NULL DEFAULT 0, total_tokens INTEGER NOT NULL DEFAULT 0, thinking_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0, n_calls INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (hour, agent_type, thinking)) WITHOUT ROWID`,
		`INSERT INTO token_usage_thinking VALUES ('2026-10-17T09','claude','high',1000,100,10,20,1130,40,1.5,9)`,
		`INSERT INTO token_usage_thinking VALUES ('2026-10-17T09','claude','low',500,50,0,0,550,0,0.5,4)`,
		`INSERT INTO token_usage_thinking VALUES ('2026-10-17T10','codex','medium',70,7,0,0,77,3,0.07,1)`,
	} {
		_, err := db.ExecContext(ctx, q)
		require.NoError(t, err)
	}
	require.NoError(t, db.ensureTokenUsageThinkingModel(ctx))
	require.NoError(t, db.ensureTokenUsageThinkingModel(ctx), "a second run leaves the table alone")

	s := NewTokenUsageStore(db)
	rows, err := s.GetUsageByThinking(ctx, "")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	var in, calls, think int64
	var cost float64
	for _, r := range rows {
		assert.Equal(t, "", r.Model, "kept under the empty model")
		in += r.InputTokens
		calls += r.NumCalls
		think += r.ThinkingTokens
		cost += r.CostUSD
	}
	assert.EqualValues(t, 1570, in)
	assert.EqualValues(t, 14, calls)
	assert.EqualValues(t, 43, think)
	assert.InDelta(t, 2.07, cost, 1e-9)

	// The new key: the same hour, type and level for two models, and the old rows still there.
	for _, m := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "n-" + m, AgentName: "a", AgentType: "claude", Model: m, Thinking: "high", Source: "jsonl",
			RecordedAt: "2026-10-17T09:30:00Z", InputTokens: 11, TotalTokens: 11}))
	}
	rows, err = s.GetUsageByThinking(ctx, "")
	require.NoError(t, err)
	assert.Len(t, rows, 5)
	var nModelRows int
	err = db.GetContext(ctx, &nModelRows, `SELECT COUNT(*) FROM pragma_table_info('token_usage_thinking') WHERE name = 'model' AND pk > 0`)
	require.NoError(t, err)
	assert.Equal(t, 1, nModelRows, "the model is part of the primary key")
}

// A new database has the model in the table from the start, and the migration does nothing to it.
func TestNewThinkingTableHasTheModelInItsKey(t *testing.T) {
	db := openTestDB(t)
	var n int
	require.NoError(t, db.GetContext(context.Background(), &n, `SELECT COUNT(*) FROM pragma_table_info('token_usage_thinking') WHERE name IN ('hour','agent_type','model','thinking') AND pk > 0`))
	assert.Equal(t, 4, n)
}

// Opening a database made before the model was in the key migrates it (the step is part of opening, not only a function): every row is kept under the empty model.
func TestOpeningAnOldDatabaseMigratesTheThinkingTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	require.NoError(t, os.WriteFile(path, migratedTemplate(t), 0o644))
	db, err := OpenWithKey(ctx, path, "")
	require.NoError(t, err)
	for _, q := range []string{
		`DROP TABLE token_usage_thinking`,
		`CREATE TABLE token_usage_thinking (hour TEXT NOT NULL, agent_type TEXT NOT NULL, thinking TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_tokens INTEGER NOT NULL DEFAULT 0, total_tokens INTEGER NOT NULL DEFAULT 0, thinking_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0, n_calls INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (hour, agent_type, thinking)) WITHOUT ROWID`,
		`INSERT INTO token_usage_thinking VALUES ('2026-10-17T09','claude','high',1000,100,10,20,1130,40,1.5,9)`,
	} {
		_, err := db.ExecContext(ctx, q)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	db, err = OpenWithKey(ctx, path, "")
	require.NoError(t, err)
	defer db.Close()
	rows, err := NewTokenUsageStore(db).GetUsageByThinking(ctx, "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "", rows[0].Model)
	assert.EqualValues(t, 1000, rows[0].InputTokens)
	assert.EqualValues(t, 9, rows[0].NumCalls)
}
