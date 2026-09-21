package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoalMetricsStore_RecordSinceAndPrune(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ms := NewGoalMetricsStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	old := &GoalGeneration{SessionID: "s1", Trigger: GoalTriggerFirst, Outcome: GoalOutcomeStored, CreatedAt: now.Add(-20 * 24 * time.Hour).Format(ISOFormat)}
	fresh := &GoalGeneration{SessionID: "s1", Trigger: GoalTriggerTurnEnd, Outcome: GoalOutcomeStored, Goal: "Fix tests", CostUSD: 0.001}
	require.NoError(t, ms.Record(ctx, old))
	require.NoError(t, ms.Record(ctx, fresh))
	assert.NotZero(t, fresh.ID)

	rows, err := ms.Since(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "Fix tests", rows[0].Goal)

	n, err := ms.Prune(ctx, now.Add(-GoalMetricsRetention))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestSummarizeGoalGenerations(t *testing.T) {
	g := func(sid, trigger, outcome string, ms int64, cost float64) GoalGeneration {
		return GoalGeneration{SessionID: sid, AgentName: "agent-" + sid, Trigger: trigger, Outcome: outcome, DurationMs: ms, CostUSD: cost, InputTokens: 400, OutputTokens: 10, CreatedAt: "t"}
	}
	rows := []GoalGeneration{
		g("a", GoalTriggerFirst, GoalOutcomeStored, 1000, 0.001),
		g("a", GoalTriggerTurnEnd, GoalOutcomeUnchanged, 2000, 0.001),
		g("a", GoalTriggerManual, GoalOutcomeTimeout, 60000, 0),
		g("b", GoalTriggerFirst, GoalOutcomeStored, 3000, 0.002),
		g("b", GoalTriggerInterval, GoalOutcomeUserGoal, 0, 0),
		g("c", GoalTriggerFirst, GoalOutcomeNoCLI, 0, 0),
	}
	m := SummarizeGoalGenerations(rows, time.Now().Add(-2*time.Hour), 2*time.Hour)

	assert.Equal(t, 6, m.Attempts)
	assert.Equal(t, 4, m.CLICalls, "user_goal and no_cli skip the CLI")
	assert.InDelta(t, 2.0, m.CLICallsPerHr, 1e-9)
	assert.Equal(t, map[string]int{"first": 3, "turn_end": 1, "manual": 1, "interval": 1}, m.ByTrigger)
	assert.Equal(t, 1, m.ByOutcome[GoalOutcomeTimeout])
	assert.InDelta(t, 0.25, m.FailureRate, 1e-9)
	assert.InDelta(t, 0.004, m.CostUSD, 1e-9)
	assert.InDelta(t, 0.001, m.AvgCostUSD, 1e-9)
	assert.Equal(t, int64(1600), m.InputTokens)
	assert.Equal(t, int64(16500), m.AvgDurationMs)
	assert.Equal(t, int64(60000), m.P95DurationMs)
	assert.Equal(t, int64(60000), m.MaxDurationMs)

	require.Len(t, m.Sessions, 3)
	assert.Equal(t, "a", m.Sessions[0].SessionID, "busiest first")
	assert.Equal(t, 3, m.Sessions[0].CLICalls)
	assert.Equal(t, GoalOutcomeTimeout, m.Sessions[0].LastOutcome)
	require.Len(t, m.RecentFailures, 1)
	assert.Equal(t, GoalOutcomeTimeout, m.RecentFailures[0].Outcome)
	assert.Len(t, m.Recent, 6)
	assert.Equal(t, GoalOutcomeNoCLI, m.Recent[0].Outcome, "newest first")

	empty := SummarizeGoalGenerations(nil, time.Now(), time.Hour)
	assert.Zero(t, empty.CLICalls)
	assert.NotNil(t, empty.Sessions)
}
