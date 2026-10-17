package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// #4179 review: a space-format row in hour 23 ("2026-08-01 23:10:00") gives the range
// ["2026-08-01 23", "2026-08-02 00"), which every ISO-format row of that date falls into
// ("2026-08-01T18:00:00Z" sorts after "2026-08-01 23" because 'T' > ' '): its bucket is mislabelled,
// and a re-read of an ISO-format row in that hour is no longer ignored by the trigger.
func TestRollupKeepsEachHourInItsOwnBucketAcrossFormats(t *testing.T) {
	ctx := context.Background()
	db, err := OpenWithKey(ctx, filepath.Join(t.TempDir(), "coral.db"), "")
	require.NoError(t, err)
	defer db.Close()
	ins := func(at string) {
		_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO token_usage (session_id, agent_name, agent_type, input_tokens, output_tokens, total_tokens, cost_usd, num_turns, recorded_at, source) VALUES ('s1','a','claude',1,1,2,1,1,?, 'jsonl')`, at)
		require.NoError(t, err)
	}
	ins("2026-08-01 23:10:00")
	ins("2026-08-01T18:00:00Z")
	ins("2026-08-01T22:30:00Z")
	_, _, err = db.RollupTokenUsage(ctx, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), 100)
	require.NoError(t, err)
	var n int
	require.NoError(t, db.GetContext(ctx, &n, `SELECT COUNT(*) FROM token_usage_hourly WHERE substr(recorded_at, 1, 13) <> hour`))
	require.Zero(t, n, "a bucket holds rows from another hour")
	ins("2026-08-01T18:00:00Z") // a re-read of an old row
	require.NoError(t, db.GetContext(ctx, &n, `SELECT COUNT(*) FROM token_usage`))
	require.Zero(t, n, "a re-read row was counted again")
}

// #4179 review (QA's cross-format point): rows in every timestamp format token_usage holds
// (RFC 3339 with Z, Coral's ISOFormat with +00:00, a space separator) over the same days and
// hours, rolled up: every total is unchanged, every hourly row holds only rows that start with
// its hour, and every row read again is ignored.
func TestTokenUsageRollupAcrossTimestampFormats(t *testing.T) {
	ctx := context.Background()
	db, err := OpenWithKey(ctx, filepath.Join(t.TempDir(), "coral.db"), "")
	require.NoError(t, err)
	defer db.Close()
	formats := []string{time.RFC3339, ISOFormat, "2006-01-02 15:04:05", "2006-01-02T15:04:05.000Z"}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	var stamps []string
	for h := 0; h < 72; h++ {
		for i, f := range formats {
			at := start.Add(time.Duration(h)*time.Hour + time.Duration(i*7+h%5)*time.Minute)
			stamps = append(stamps, at.Format(f))
		}
	}
	ins := func(at string, n int) {
		_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO token_usage (session_id, agent_name, agent_type, input_tokens, output_tokens, total_tokens, cost_usd, num_turns, recorded_at, source) VALUES ('s1','a','claude',?,1,2,0.5,1,?, 'jsonl')`, n, at)
		require.NoError(t, err)
	}
	for i, at := range stamps {
		ins(at, 10+i%9)
	}
	s := NewTokenUsageStore(db)
	before, err := s.GetSessionUsage(ctx, "s1")
	require.NoError(t, err)

	_, rows, err := db.RollupTokenUsage(ctx, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), 10000)
	require.NoError(t, err)
	require.EqualValues(t, len(stamps), rows, "every row rolled up once")
	var raw, wrong int
	require.NoError(t, db.GetContext(ctx, &raw, `SELECT COUNT(*) FROM token_usage`))
	require.Zero(t, raw)
	require.NoError(t, db.GetContext(ctx, &wrong, `SELECT COUNT(*) FROM token_usage_hourly WHERE substr(recorded_at, 1, 13) <> hour`))
	require.Zero(t, wrong, "a bucket holds rows from another hour")
	after, err := s.GetSessionUsage(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, before, after, "totals unchanged")

	for _, at := range stamps { // a transcript read again
		ins(at, 99)
	}
	require.NoError(t, db.GetContext(ctx, &raw, `SELECT COUNT(*) FROM token_usage`))
	require.Zero(t, raw, "every re-read row is ignored")
	again, err := s.GetSessionUsage(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, before, again)
}
