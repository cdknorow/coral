package store

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// seedTokenUsage writes perSession turns for n sessions. Every fifth session
// also has proxy rows (which the jsonl rows must win over) and every seventh
// is proxy-only. Returns the session ids.
func seedTokenUsage(t testing.TB, db *DB, n, perSession int, seed int64) []string {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	tx, err := db.Beginx()
	require.NoError(t, err)
	stmt, err := tx.Preparex(`INSERT OR IGNORE INTO token_usage
		(session_id, agent_name, agent_type, board_name, input_tokens, output_tokens, cache_read_tokens,
		 cache_write_tokens, total_tokens, cost_usd, num_turns, session_start_at, last_activity_at, recorded_at, source)
		VALUES (?, ?, 'claude', ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`)
	require.NoError(t, err)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	ids := make([]string, n)
	for s := 0; s < n; s++ {
		sid := fmt.Sprintf("tok-%05d", s)
		ids[s] = sid
		at := base.Add(time.Duration(s) * time.Minute)
		for i := 0; i < perSession; i++ {
			at = at.Add(time.Duration(1+rng.Intn(20)) * time.Second)
			source := "jsonl"
			switch {
			case s%7 == 3:
				source = "proxy"
			case s%5 == 0 && rng.Intn(3) == 0:
				source = "proxy"
			}
			in, out, read, write := rng.Intn(5000), rng.Intn(2000), rng.Intn(90000), rng.Intn(4000)
			stamp := at.Format(ISOFormat)
			_, err := stmt.Exec(sid, fmt.Sprintf("agent-%d", s), fmt.Sprintf("board-%d", s%4),
				in, out, read, write, in+out+read+write, float64(rng.Intn(1000))/1000,
				base.Format(ISOFormat), stamp, stamp, source)
			require.NoError(t, err)
		}
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
	return ids
}

// The per-tick queries before #2918, kept to prove the rewrites agree.
const legacySourceDedup = `(COALESCE(source,'jsonl') = 'jsonl' OR session_id NOT IN (SELECT DISTINCT session_id FROM token_usage WHERE source = 'jsonl'))`

func legacyLatestUsage(t testing.TB, db *DB, ids []string) map[string]*TokenUsage {
	query, args, err := sqlx.In(
		`SELECT MAX(id) as id, session_id, MAX(agent_name) as agent_name, MAX(agent_type) as agent_type,
		 MAX(team_id) as team_id, MAX(board_name) as board_name,
		 COALESCE(SUM(input_tokens), 0) as input_tokens, COALESCE(SUM(output_tokens), 0) as output_tokens,
		 COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens, COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
		 COALESCE(SUM(total_tokens), 0) as total_tokens, COALESCE(SUM(cost_usd), 0) as cost_usd,
		 COUNT(*) as num_turns, MAX(recorded_at) as recorded_at
		 FROM token_usage WHERE session_id IN (?) AND `+legacySourceDedup+`
		 GROUP BY session_id`, ids)
	require.NoError(t, err)
	var records []TokenUsage
	require.NoError(t, db.Select(&records, query, args...))
	out := map[string]*TokenUsage{}
	for i := range records {
		out[records[i].SessionID] = &records[i]
	}
	return out
}

func legacyLatestTurnContext(t testing.TB, db *DB, ids []string) map[string]int {
	qualified := `(COALESCE(t.source,'jsonl') = 'jsonl' OR t.session_id NOT IN (SELECT DISTINCT session_id FROM token_usage WHERE source = 'jsonl'))`
	query, args, err := sqlx.In(
		`SELECT t.session_id, (t.input_tokens + t.cache_read_tokens + t.cache_write_tokens) as context_tokens
		 FROM token_usage t
		 INNER JOIN (
		     SELECT session_id, MAX(recorded_at) as max_at
		     FROM token_usage
		     WHERE session_id IN (?) AND `+legacySourceDedup+`
		     GROUP BY session_id
		 ) latest ON t.session_id = latest.session_id AND t.recorded_at = latest.max_at
		 WHERE t.session_id IN (?) AND `+qualified, ids, ids)
	require.NoError(t, err)
	var rows []struct {
		SessionID     string `db:"session_id"`
		ContextTokens int    `db:"context_tokens"`
	}
	require.NoError(t, db.Select(&rows, query, args...))
	out := map[string]int{}
	for _, r := range rows {
		out[r.SessionID] = r.ContextTokens
	}
	return out
}

// requireSameUsage compares the sums with a tolerance on cost: a running
// float total and a fresh SUM can differ in the last bits.
func requireSameUsage(t *testing.T, want, got map[string]*TokenUsage) {
	t.Helper()
	require.Len(t, got, len(want))
	for id, w := range want {
		g, ok := got[id]
		require.True(t, ok, "missing session %s", id)
		require.InDelta(t, w.CostUSD, g.CostUSD, 1e-6, "session %s cost", id)
		wc, gc := *w, *g
		wc.CostUSD, gc.CostUSD = 0, 0
		require.Equal(t, wc, gc, "session %s", id)
	}
}

func TestTokenUsageLatestQueriesMatchLegacy(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := t.Context()
	ids := seedTokenUsage(t, db, 40, 60, 3)
	query := append(append([]string{}, ids...), "unknown", ids[0]) // unknown and repeated ids
	check := func() {
		t.Helper()
		got, err := s.GetLatestUsageBySessionIDs(ctx, query)
		require.NoError(t, err)
		requireSameUsage(t, legacyLatestUsage(t, db, query), got)
		turn, err := s.GetLatestTurnContextBySessionIDs(ctx, query)
		require.NoError(t, err)
		require.Equal(t, legacyLatestTurnContext(t, db, query), turn)
	}
	check()

	// A proxy-only session gains its first jsonl row: from then on only jsonl counts.
	proxyOnly := ids[3]
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: proxyOnly, AgentName: "x", InputTokens: 7, CostUSD: 0.5, RecordedAt: "2026-08-01T00:00:00.000000Z"}))
	check()
	// Duplicates are ignored, repricing a zero-cost row and team ids count.
	team := int64(9)
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: ids[1], AgentName: "zz-late", TeamID: &team, Model: "m", RecordedAt: "2027-01-01T00:00:00.000000Z", InputTokens: 11}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: ids[1], AgentName: "dup", RecordedAt: "2027-01-01T00:00:00.000000Z", InputTokens: 999}))
	require.NoError(t, s.RepriceZeroCosts(ctx, func(string, int, int, int, int) float64 { return 1.25 }))
	check()
	// A brand-new proxy session.
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "new-proxy", AgentName: "p", Source: "proxy", OutputTokens: 3}))
	query = append(query, "new-proxy")
	check()
}

// TestTokenUsageLatestPerf times the per-tick token_usage queries. The table
// is never pruned, so it holds history for many more sessions than are live.
// Skipped unless CORAL_PERF_TOKENS is set.
func TestTokenUsageLatestPerf(t *testing.T) {
	if os.Getenv("CORAL_PERF_TOKENS") == "" {
		t.Skip("set CORAL_PERF_TOKENS=1 to run")
	}
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	all := seedTokenUsage(t, db, 300, 1000, 5) // 300k turns
	_, err := db.Exec("ANALYZE")
	require.NoError(t, err)
	live := all[len(all)-50:]
	ctx := t.Context()
	timeIt := func(name string, fn func()) {
		fn()
		var d []time.Duration
		for i := 0; i < 15; i++ {
			start := time.Now()
			fn()
			d = append(d, time.Since(start))
		}
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		t.Logf("%-34s p50=%-10s max=%s", name, d[len(d)/2].Round(10*time.Microsecond), d[len(d)-1].Round(10*time.Microsecond))
	}
	timeIt("GetLatestUsageBySessionIDs(50)", func() { _, _ = s.GetLatestUsageBySessionIDs(ctx, live) })
	timeIt("GetLatestTurnContextBySessionIDs(50)", func() { _, _ = s.GetLatestTurnContextBySessionIDs(ctx, live) })
	timeIt("GetSessionUsage(1)", func() { _, _ = s.GetSessionUsage(ctx, live[0]) })
	n := 0
	timeIt("RecordUsage", func() {
		n++
		_ = s.RecordUsage(ctx, &TokenUsage{SessionID: live[1], AgentName: "a", InputTokens: 1, RecordedAt: fmt.Sprintf("2030-01-01T00:00:%02d.%06dZ", n/1000000, n%1000000)})
	})
}

// Rows written through any path (raw UPDATE/DELETE, a key change) keep the
// rollup equal to a recount of token_usage.
func TestTokenUsageRollupFollowsUpdatesAndDeletes(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := t.Context()
	ids := seedTokenUsage(t, db, 15, 40, 11)
	check := func() {
		t.Helper()
		got, err := s.GetLatestUsageBySessionIDs(ctx, ids)
		require.NoError(t, err)
		requireSameUsage(t, legacyLatestUsage(t, db, ids), got)
		turn, err := s.GetLatestTurnContextBySessionIDs(ctx, ids)
		require.NoError(t, err)
		require.Equal(t, legacyLatestTurnContext(t, db, ids), turn)
	}
	for _, stmt := range []string{
		// Token counts change on the newest turn of a session.
		`UPDATE token_usage SET input_tokens = input_tokens + 100 WHERE id = (SELECT MAX(id) FROM token_usage WHERE session_id = 'tok-00001')`,
		// Every jsonl row of a mixed session becomes proxy, then one comes back.
		`UPDATE token_usage SET source = 'proxy' WHERE session_id = 'tok-00005'`,
		`UPDATE token_usage SET source = 'jsonl' WHERE id = (SELECT MIN(id) FROM token_usage WHERE session_id = 'tok-00005')`,
		// Rows move between sessions and the newest turn is deleted.
		`UPDATE token_usage SET session_id = 'tok-00002' WHERE session_id = 'tok-00006' AND recorded_at NOT IN (SELECT recorded_at FROM token_usage WHERE session_id = 'tok-00002')`,
		`DELETE FROM token_usage WHERE id = (SELECT MAX(id) FROM token_usage WHERE session_id = 'tok-00007')`,
		`UPDATE token_usage SET agent_name = 'renamed', board_name = NULL WHERE session_id = 'tok-00008'`,
		`DELETE FROM token_usage WHERE session_id = 'tok-00009'`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
		check()
	}
}

// Opening a database that predates the rollup fills it from the history.
func TestTokenUsageRollupBackfillsExistingDatabase(t *testing.T) {
	path := t.TempDir() + "/sessions.db"
	db, err := OpenWithKey(context.Background(), path, "")
	require.NoError(t, err)
	ids := seedTokenUsage(t, db, 20, 50, 13)
	for _, stmt := range []string{
		`DROP TRIGGER token_usage_rollup_insert`, `DROP TRIGGER token_usage_rollup_update_delta`,
		`DROP TRIGGER token_usage_rollup_update_rebuild`, `DROP TRIGGER token_usage_rollup_delete`,
		`DROP TABLE token_usage_rollup`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}
	// Written while no rollup exists, as an older binary would.
	_, err = db.Exec(`INSERT INTO token_usage (session_id, agent_name, input_tokens, recorded_at) VALUES ('tok-00001', 'old', 5, '2031-01-01T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = OpenWithKey(context.Background(), path, "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s := NewTokenUsageStore(db)
	got, err := s.GetLatestUsageBySessionIDs(t.Context(), ids)
	require.NoError(t, err)
	requireSameUsage(t, legacyLatestUsage(t, db, ids), got)
	turn, err := s.GetLatestTurnContextBySessionIDs(t.Context(), ids)
	require.NoError(t, err)
	require.Equal(t, legacyLatestTurnContext(t, db, ids), turn)

	// A second open does not backfill again.
	require.NoError(t, db.Close())
	db, err = OpenWithKey(context.Background(), path, "")
	require.NoError(t, err)
	got, err = NewTokenUsageStore(db).GetLatestUsageBySessionIDs(t.Context(), ids)
	require.NoError(t, err)
	requireSameUsage(t, legacyLatestUsage(t, db, ids), got)
}
