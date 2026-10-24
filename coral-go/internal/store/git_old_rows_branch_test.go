package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6832 (Security's #6836 point 2): the two readers with their own SQL on git_snapshots.branch clean a row stored before the write path did, and the cost-by-branch rollup merges two raw spellings of one
// branch into one row.
func TestOldRawBranchesAreCleanedInTheHistoryListAndTheCostRollup(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	usage := NewTokenUsageStore(db)
	insert := func(sid, branch string) {
		_, err := db.ExecContext(ctx, `INSERT INTO git_snapshots (agent_name, agent_type, working_directory, branch, commit_hash, commit_subject, commit_timestamp, session_id, recorded_at)
			VALUES ('repo', 'claude', '/tmp/r', ?, ?, 's', '2026-10-01T10:00:00Z', ?, '2026-10-01T10:00:00Z')`, branch, "hash-"+sid, sid)
		require.NoError(t, err)
	}
	insert("s1", "feature/\u202ex\x1b[31m") // two raw spellings of one branch, written before the write path cleaned
	insert("s2", "feature/\u200bx")
	insert("s3", "main")
	for sid, cost := range map[string]float64{"s1": 3, "s2": 4, "s3": 1} {
		require.NoError(t, usage.RecordUsage(ctx, &TokenUsage{SessionID: sid, AgentName: "a", AgentType: "claude", RecordedAt: "2026-10-01T10:00:00Z", InputTokens: 10, TotalTokens: 10, CostUSD: cost}))
	}

	rows, err := usage.GetUsageSummaryByBranch(ctx, "", "")
	require.NoError(t, err)
	require.Len(t, rows, 2, "the two spellings merge: %+v", rows)
	assert.Equal(t, "feature/x", rows[0].Branch)
	assert.EqualValues(t, 7, rows[0].CostUSD, "3 + 4 added, the biggest spender first")
	assert.EqualValues(t, 20, rows[0].TotalTokens)
	assert.Equal(t, "main", rows[1].Branch)

	branches := NewSessionStore(db).sessionLatestBranches(ctx, []string{"s1", "s2", "s3"})
	assert.Equal(t, map[string]string{"s1": "feature/x", "s2": "feature/x", "s3": "main"}, branches)
}
