package background

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/require"
)

func healthMonitorStore(t *testing.T) *board.Store {
	t.Helper()
	s, err := board.NewStore(filepath.Join(t.TempDir(), "board-health.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func healthMonitorBoard(t *testing.T, s *board.Store, project string) {
	t.Helper()
	ctx := context.Background()
	_, err := s.Subscribe(ctx, project, "Orchestrator", "Orchestrator", "session-"+project, nil, nil, "")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, project, "Worker", "Worker", "worker-"+project, nil, nil, "")
	require.NoError(t, err)
}

func healthMonitorIssue(t *testing.T, s *board.Store, project string) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, err := s.CreateTask(ctx, project, "unassigned task", "", "medium", "Orchestrator")
		require.NoError(t, err)
	}
}

func TestBoardHealthMonitor_DefaultIntervalIsTenMinutes(t *testing.T) {
	m := NewBoardHealthMonitor(healthMonitorStore(t), 0)
	require.Equal(t, 10*time.Minute, m.interval)
}

func TestBoardHealthMonitor_RunRepeatsAndDeduplicatesReports(t *testing.T) {
	s := healthMonitorStore(t)
	healthMonitorBoard(t, s, "repeat")
	healthMonitorIssue(t, s, "repeat")

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	m := NewBoardHealthMonitor(s, time.Millisecond)
	require.ErrorIs(t, m.Run(ctx), context.DeadlineExceeded)

	messages, err := s.ListMessages(context.Background(), "repeat", 100, 0, 0)
	require.NoError(t, err)
	// Run performs an immediate scan and repeated ticker scans, but identical
	// findings are intentionally delivered once until the report changes.
	require.Len(t, messages, 1)
	require.Contains(t, messages[0].Content, "unblocked tasks can potentially run in parallel")
}

func TestBoardHealthMonitor_IsolatesReportsByBoard(t *testing.T) {
	s := healthMonitorStore(t)
	healthMonitorBoard(t, s, "alpha")
	healthMonitorBoard(t, s, "beta")
	healthMonitorIssue(t, s, "alpha")
	healthMonitorIssue(t, s, "beta")

	m := NewBoardHealthMonitor(s, time.Hour)
	m.scan(context.Background())

	alpha, err := s.ListMessages(context.Background(), "alpha", 100, 0, 0)
	require.NoError(t, err)
	beta, err := s.ListMessages(context.Background(), "beta", 100, 0, 0)
	require.NoError(t, err)
	require.Len(t, alpha, 1)
	require.Len(t, beta, 1)
	require.Equal(t, "alpha", alpha[0].Project)
	require.Equal(t, "beta", beta[0].Project)
}

func TestBoardHealthMonitor_NoIssuesProducesNoHeartbeat(t *testing.T) {
	s := healthMonitorStore(t)
	healthMonitorBoard(t, s, "quiet")

	m := NewBoardHealthMonitor(s, time.Hour)
	m.scan(context.Background())

	count, err := s.CountMessages(context.Background(), "quiet")
	require.NoError(t, err)
	// This monitor reports findings and idle reminders; it is not a periodic
	// job-status heartbeat when a board is healthy.
	require.Zero(t, count)
}
