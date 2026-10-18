package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// #4752: the token usage reads (the Cost view's among them) run on the read pool. They ran on the primary connection, the only one writers have
// (MaxOpenConns 1): a 2 to 6 s Cost query then held every other write of sessions.db behind it (the hooks' events, the indexer, the pollers)
// and the five queries of the view ran one after the other. With the primary connection held, each read still answers at once.
func TestTokenUsageReadsDoNotNeedThePrimaryConnection(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "s1", AgentName: "a", AgentType: "claude", RecordedAt: "2026-10-01T10:00:00Z", InputTokens: 10, TotalTokens: 10, CostUSD: 1}))

	conn, err := db.DB.Connx(ctx) // the one connection writers use
	require.NoError(t, err)
	defer conn.Close()

	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	for name, call := range map[string]func() error{
		"GetSessionUsage":         func() error { _, e := s.GetSessionUsage(rctx, "s1"); return e },
		"GetTeamUsage":            func() error { _, e := s.GetTeamUsage(rctx, 1); return e },
		"GetBoardUsage":           func() error { _, e := s.GetBoardUsage(rctx, "b"); return e },
		"GetUsageSummary":         func() error { _, e := s.GetUsageSummary(rctx, ""); return e },
		"GetUsageSummaryByModel":  func() error { _, e := s.GetUsageSummaryByModel(rctx, ""); return e },
		"GetUsageSummaryByAgent":  func() error { _, e := s.GetUsageSummaryByAgent(rctx, ""); return e },
		"ListUsage":               func() error { _, e := s.ListUsage(rctx, UsageFilter{}); return e },
		"GetSessionTurns":         func() error { _, e := s.GetSessionTurns(rctx, "s1"); return e },
		"GetUsageTimeSeries":      func() error { _, e := s.GetUsageTimeSeries(rctx, "", "1h"); return e },
		"GetUsageSummaryByBranch": func() error { _, e := s.GetUsageSummaryByBranch(rctx, "", ""); return e },
		"GetUsageSummaryByBoard":  func() error { _, e := s.GetUsageSummaryByBoard(rctx, ""); return e },
	} {
		require.NoError(t, call(), name+" waited for the primary connection")
	}
	require.Less(t, time.Since(start), 2*time.Second, "the reads did not wait for the held primary connection")
}
