package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// #4752: the Cost view reads the hourly aggregate where it is current and the raw rows everywhere else. Old against new: the same usage, read
// through the aggregate and read with it switched off (every query then reads token_usage_all as before), must give the same answers for every
// Cost query, every window, at every stage of the aggregate's life (not built yet, built, rows added late to a built hour, changed and deleted,
// rows in other timestamp layouts). The sums are compared to a millionth of a cent: the aggregate adds the same numbers in another order.

var aggNow = time.Date(2026, 10, 18, 14, 20, 0, 0, time.UTC)

type aggSeed struct {
	t   *testing.T
	db  *DB
	rng *rand.Rand
	n   int
}

func (a *aggSeed) insert(sess string, at string, source string) {
	a.t.Helper()
	a.n++
	k := a.rng.Intn(12)
	sessions := a.sessionInfo(sess)
	var model any
	if m := []string{"opus", "sonnet", "haiku", ""}[a.rng.Intn(4)]; m != "" {
		model = m
	}
	_, err := a.db.ExecContext(context.Background(),
		`INSERT INTO token_usage (session_id, agent_name, agent_type, team_id, board_name, input_tokens, output_tokens, total_tokens, cost_usd, num_turns, recorded_at, cache_read_tokens, cache_write_tokens, source, model)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
		sess, sessions.name, sessions.typ, sessions.team, sessions.board, 10+k, 20+k, 30+2*k, float64(a.rng.Intn(500))/100, at, k*3, k, source, model)
	require.NoError(a.t, err)
}

type aggSession struct {
	name, typ string
	team      any
	board     any
}

func (a *aggSeed) sessionInfo(sess string) aggSession {
	var i int
	fmt.Sscanf(sess, "s%d", &i)
	boards := []any{"alpha", "beta", nil, ""}
	var team any
	if i%3 == 0 {
		team = int64(i)
	}
	return aggSession{name: fmt.Sprintf("agent %d", i), typ: []string{"claude", "gemini"}[i%2], team: team, board: boards[i%4]}
}

func storeLayout(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000") + "+00:00" }

func (a *aggSeed) randomTimeIn(from, to time.Time) time.Time {
	return from.Add(time.Duration(a.rng.Int63n(int64(to.Sub(from)))))
}

func normalizeJSON(t *testing.T, v any) any {
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var out any
	require.NoError(t, json.Unmarshal(b, &out))
	var round func(any) any
	round = func(x any) any {
		switch y := x.(type) {
		case float64:
			return math.Round(y*1e6) / 1e6
		case []any:
			for i := range y {
				y[i] = round(y[i])
			}
		case map[string]any:
			for k := range y {
				y[k] = round(y[k])
			}
		}
		return x
	}
	return round(out)
}

type costAnswers map[string]any

func costAnswersFor(t *testing.T, s *TokenUsageStore, since string) costAnswers {
	ctx := context.Background()
	out := costAnswers{}
	var err error
	out["summary"], err = s.GetUsageSummary(ctx, since)
	require.NoError(t, err)
	out["model"], err = s.GetUsageSummaryByModel(ctx, since)
	require.NoError(t, err)
	out["agent"], err = s.GetUsageSummaryByAgent(ctx, since)
	require.NoError(t, err)
	out["board"], err = s.GetUsageSummaryByBoard(ctx, since)
	require.NoError(t, err)
	out["branch"], err = s.GetUsageSummaryByBranch(ctx, since, "")
	require.NoError(t, err)
	for _, iv := range []string{"1h", "1d", "5m", ""} {
		out["series "+iv], err = s.GetUsageTimeSeries(ctx, since, iv)
		require.NoError(t, err)
	}
	return out
}

// oldSummary and oldSeries are the queries as they were before the aggregate, over token_usage_all with the dedup subquery over the same view.
func oldSummary(t *testing.T, db *DB, since string) []UsageSummary {
	q := `SELECT agent_type, COALESCE(SUM(input_tokens), 0) as input_tokens, COALESCE(SUM(output_tokens), 0) as output_tokens,
	      COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens, COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
	      COALESCE(SUM(total_tokens), 0) as total_tokens, COALESCE(SUM(cost_usd), 0) as cost_usd, COUNT(DISTINCT session_id) as num_sessions
	      FROM token_usage_all WHERE ` + sourceDedup + ` AND 1=1`
	var args []any
	if since != "" {
		q += " AND recorded_at >= ?"
		args = append(args, since)
	}
	q += ` GROUP BY agent_type`
	var out []UsageSummary
	require.NoError(t, db.SelectContext(context.Background(), &out, q, args...))
	return out
}

func oldSeries1h(t *testing.T, db *DB, since string) []TimeSeriesBucket {
	q := `SELECT strftime('%Y-%m-%dT%H:00:00Z', recorded_at) as bucket, COALESCE(SUM(cost_usd), 0) as cost_usd, COALESCE(SUM(input_tokens), 0) as input_tokens,
	      COALESCE(SUM(output_tokens), 0) as output_tokens, COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
	      COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens, SUM(n_rows) as num_requests
	      FROM token_usage_all WHERE ` + sourceDedup + ` AND 1=1`
	var args []any
	if since != "" {
		q += " AND recorded_at >= ?"
		args = append(args, since)
	}
	q += ` GROUP BY bucket ORDER BY bucket ASC`
	var out []TimeSeriesBucket
	require.NoError(t, db.SelectContext(context.Background(), &out, q, args...))
	return out
}

func aggSinces() []string {
	return []string{
		"",
		"2026-10-16T14:00:00Z",             // on an hour
		"2026-10-16T14:37:12.5Z",           // inside an hour
		"2026-10-16T14:37:12+00:00",        // the other canonical suffix
		"2026-10-17T00:00:00.000000+00:00", // the stored layout, on a day
		"2026-10-16T16:37:12+02:00",        // not UTC: compared as text, so the raw rows answer
		"2026-10-16 14:00:00",              // a space for the T
		"2026-10-11T03:20:00Z",             // inside the rolled-up (hourly) rows
		"2020-01-01T00:00:00Z",             // before everything
		"2026-10-18T14:00:00Z",             // the hour still being written
		"2026-12-01T00:00:00Z",             // after everything
	}
}

func TestCostViewAggregateMatchesTheRawRows(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()
	a := &aggSeed{t: t, db: db, rng: rand.New(rand.NewSource(4752))}

	// 12 sessions: 0-7 jsonl only, 8-9 hook only, 10-11 both (the hook rows must not count).
	srcOf := func(i int) []string {
		switch {
		case i < 8:
			return []string{"jsonl"}
		case i < 10:
			return []string{"hook"}
		}
		return []string{"jsonl", "hook"}
	}
	start := aggNow.Add(-200 * time.Hour)
	for i := 0; i < 12; i++ {
		for _, src := range srcOf(i) {
			for r := 0; r < 140; r++ {
				at := a.randomTimeIn(start, aggNow)
				layout := storeLayout(at)
				switch a.rng.Intn(10) {
				case 0:
					layout = at.Format("2006-01-02T15:04:05.000Z")
				case 1:
					layout = at.Format("2006-01-02T15:04:05Z")
				}
				a.insert(fmt.Sprintf("s%d", i), layout, src)
			}
		}
	}
	// branches for the by-branch view
	for i := 0; i < 12; i += 2 {
		_, err := db.ExecContext(ctx, `INSERT INTO git_snapshots (agent_name, agent_type, working_directory, branch, commit_hash, session_id, remote_url, recorded_at) VALUES (?, 'claude', '/w', ?, ?, ?, 'https://x/y', '2026-10-18T10:00:00Z')`,
			fmt.Sprintf("agent %d", i), fmt.Sprintf("branch-%d", i%4), fmt.Sprintf("c%d", i), fmt.Sprintf("s%d", i))
		require.NoError(t, err)
	}
	// the oldest 100 hours become hourly rows, as the retention job does
	_, rolled, err := db.RollupTokenUsage(ctx, aggNow.Add(-100*time.Hour), 1000)
	require.NoError(t, err)
	require.Greater(t, rolled, int64(500), "the seed has rows old enough to be rolled up")

	var compared, withAgg, nonEmpty int
	compare := func(stage string, wantAgg bool) {
		t.Helper()
		for _, since := range aggSinces() {
			tokenUsageHourAggOn = true
			got := costAnswersFor(t, s, since)
			frag, _ := usageRelation(ctx, db, since)
			tokenUsageHourAggOn = false
			want := costAnswersFor(t, s, since)
			tokenUsageHourAggOn = true
			for k := range want {
				require.Equal(t, normalizeJSON(t, want[k]), normalizeJSON(t, got[k]), "%s: since %q, %s", stage, since, k)
				if arr, ok := want[k].([]ModelUsageSummary); ok && len(arr) > 0 {
					nonEmpty++
				}
			}
			// independent of the shared query text: the pre-aggregate SQL itself
			require.Equal(t, normalizeJSON(t, oldSummary(t, db, since)), normalizeJSON(t, got["summary"]), "%s: since %q, summary against the old SQL", stage, since)
			require.Equal(t, normalizeJSON(t, oldSeries1h(t, db, since)), normalizeJSON(t, got["series 1h"]), "%s: since %q, series against the old SQL", stage, since)
			compared++
			if strings.Contains(frag, "token_usage_hour_agg") {
				withAgg++
			}
		}
		if wantAgg {
			require.Greater(t, withAgg, 0, stage+": the aggregate answered some windows")
		}
	}

	compare("before the first refresh", false)
	require.Zero(t, withAgg, "an aggregate that is empty is not read")

	n, err := db.RefreshTokenUsageHourAgg(ctx, aggNow, 100000)
	require.NoError(t, err)
	require.Greater(t, n, 40)
	var left int
	require.NoError(t, db.GetContext(ctx, &left, `SELECT COUNT(*) FROM token_usage_hour_dirty`))
	require.Equal(t, 1, left, "only the hour still being written is left dirty")
	withAgg = 0
	compare("built", true)
	built := withAgg
	require.GreaterOrEqual(t, built, 6, "the windows that start in an hour already built read it")

	// Each kind of change to a built hour, alone, so its trigger is the only thing that can have marked the hour: a row added late, a row changed
	// in place, a row moved to another hour, a row deleted.
	refresh := func() {
		_, err := db.RefreshTokenUsageHourAgg(ctx, aggNow, 100000)
		require.NoError(t, err)
	}
	change := func(stage string, do func()) {
		t.Helper()
		refresh()
		require.NoError(t, db.GetContext(ctx, &left, `SELECT COUNT(*) FROM token_usage_hour_dirty`))
		require.Equal(t, 1, left)
		do()
		require.NoError(t, db.GetContext(ctx, &left, `SELECT COUNT(*) FROM token_usage_hour_dirty`))
		require.Greater(t, left, 1, stage+": the change marked its hour")
		withAgg = 0
		compare(stage+", before the refresh", true)
		refresh()
		withAgg = 0
		compare(stage+", after the refresh", true)
	}
	change("a row added late", func() {
		a.insert("s4", storeLayout(aggNow.Add(-50*time.Hour+7*time.Minute)), "jsonl")
	})
	change("a row changed in place", func() {
		_, err := db.ExecContext(ctx, `UPDATE token_usage SET cost_usd = cost_usd + 7, input_tokens = input_tokens + 5 WHERE id = (SELECT MAX(id) FROM token_usage WHERE session_id = 's1' AND recorded_at < ? AND recorded_at > ?)`,
			storeLayout(aggNow.Add(-40*time.Hour)), storeLayout(aggNow.Add(-70*time.Hour)))
		require.NoError(t, err)
	})
	change("a row moved to another hour", func() {
		_, err := db.ExecContext(ctx, `UPDATE token_usage SET recorded_at = ? WHERE id = (SELECT MIN(id) FROM token_usage WHERE session_id = 's5' AND recorded_at > ?)`,
			storeLayout(aggNow.Add(-30*time.Hour)), storeLayout(aggNow.Add(-90*time.Hour)))
		require.NoError(t, err)
	})
	change("a row deleted", func() {
		_, err := db.ExecContext(ctx, `DELETE FROM token_usage WHERE id = (SELECT MAX(id) FROM token_usage WHERE session_id = 's2' AND recorded_at < ? AND recorded_at > ?)`,
			storeLayout(aggNow.Add(-60*time.Hour)), storeLayout(aggNow.Add(-90*time.Hour)))
		require.NoError(t, err)
	})

	// rows in other layouts inside built hours: the hour is listed odd and read from the raw rows from then on
	for _, at := range []string{"2026-10-17T09:15:00+02:00", "2026-10-17 12:30:00", "2026-10-17T20:45:10-05:00", "2026-10-17T13:00:00.5Z"} {
		a.insert("s3", at, "jsonl")
	}
	_, err = db.RefreshTokenUsageHourAgg(ctx, aggNow, 100000)
	require.NoError(t, err)
	withAgg = 0
	compare("with rows in other layouts", true)
	var odd int
	require.NoError(t, db.GetContext(ctx, &odd, `SELECT COUNT(*) FROM token_usage_hour_odd`))
	require.GreaterOrEqual(t, odd, 3)

	// a restart builds nothing twice and changes no answer
	require.NoError(t, db.ensureTokenUsageHourAgg(ctx))
	withAgg = 0
	compare("after another start", true)
	require.Greater(t, compared, 40)
	require.Greater(t, nonEmpty, compared*3/4, "the comparisons are not all of empty answers (only a window after everything is)")
}
