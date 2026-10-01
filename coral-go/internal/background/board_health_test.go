package background

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type healthRuntimeProbe struct {
	mockRuntime
	captureCalls int
}

func (p *healthRuntimeProbe) CaptureActivity(context.Context, string) (string, error) {
	p.captureCalls++
	return "SECRET_TERMINAL_OUTPUT", nil
}

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

func TestBoardHealthMonitor_ActiveWorkProducesPeriodicStatus(t *testing.T) {
	s := healthMonitorStore(t)
	healthMonitorBoard(t, s, "active")
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "active", "Build status", "", "medium", "Orchestrator", "Worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "active", "Worker", task.ID)
	require.NoError(t, err)

	m := NewBoardHealthMonitor(s, time.Hour)
	m.scan(ctx)
	m.scan(ctx)

	messages, err := s.ListMessages(ctx, "active", 100, 0, 0)
	require.NoError(t, err)
	require.Len(t, messages, 2, "each monitor cadence emits one active-work status")
	require.Contains(t, messages[0].Content, "active work: 1 in_progress task(s) across 1 agent(s)")
	require.Contains(t, messages[0].Content, "Worker (1)")
	require.NotContains(t, messages[0].Content, "@Worker")
	workerMessages, err := s.ReadMessages(ctx, "active", "Worker", 50)
	require.NoError(t, err)
	require.Empty(t, workerMessages, "assignee counts must not create worker mentions")
}

func TestBoardHealthMonitor_PersistentFindingDoesNotStarveStatus(t *testing.T) {
	s := healthMonitorStore(t)
	healthMonitorBoard(t, s, "finding-active")
	ctx := context.Background()
	active, err := s.CreateTask(ctx, "finding-active", "Active", "", "medium", "Orchestrator", "Worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "finding-active", "Worker", active.ID)
	require.NoError(t, err)
	healthMonitorIssue(t, s, "finding-active")

	m := NewBoardHealthMonitor(s, time.Hour)
	m.scan(ctx)
	m.scan(ctx)
	messages, err := s.ListMessages(ctx, "finding-active", 100, 0, 0)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Contains(t, messages[0].Content, "[Coral board health]")
	require.Contains(t, messages[1].Content, "[Coral team status]")
}

func TestBoardHealthMonitor_ConsolidatesFindingsAndStatusPerScan(t *testing.T) {
	s := healthMonitorStore(t)
	healthMonitorBoard(t, s, "combined")
	ctx := context.Background()
	active, err := s.CreateTask(ctx, "combined", "Idle active work", "", "medium", "Orchestrator", "Worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "combined", "Worker", active.ID)
	require.NoError(t, err)
	healthMonitorIssue(t, s, "combined")

	m := NewBoardHealthMonitor(s, time.Hour)
	m.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	m.SetIdleThresholds(time.Hour, 3*time.Hour)
	m.scan(ctx)
	messages, err := s.ListMessages(ctx, "combined", 100, 0, 0)
	require.NoError(t, err)
	require.Len(t, messages, 1, "same-scan findings and status must be one report")
	require.Contains(t, messages[0].Content, "unblocked tasks can potentially run in parallel")
	require.Contains(t, messages[0].Content, "[Task #")
	require.Contains(t, messages[0].Content, "active work: 1 in_progress task(s) across 1 agent(s)")
	require.Contains(t, messages[0].Content, "planned work: 0 queued assignment(s)")

	m.scan(ctx)
	messages, err = s.ListMessages(ctx, "combined", 100, 0, 0)
	require.NoError(t, err)
	require.Len(t, messages, 2, "active summary recurs while deduped findings stay quiet")
	require.NotContains(t, messages[1].Content, "[Task #")
}

func TestBoardHealthMonitor_UsesRegisteredOrchestratorAndSkipsAbsent(t *testing.T) {
	ctx := context.Background()
	custom := healthMonitorStore(t)
	_, err := custom.Subscribe(ctx, "custom", "Lead Coordinator", "Orchestrator", "custom-orch", nil, nil, "")
	require.NoError(t, err)
	task, err := custom.CreateTask(ctx, "custom", "Active", "", "medium", "Lead Coordinator", "Worker")
	require.NoError(t, err)
	_, err = custom.ClaimTask(ctx, "custom", "Worker", task.ID)
	require.NoError(t, err)
	m := NewBoardHealthMonitor(custom, time.Hour)
	m.scan(ctx)
	messages, err := custom.ListMessages(ctx, "custom", 100, 0, 0)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Contains(t, messages[0].Content, "@Lead Coordinator")
	require.NotContains(t, messages[0].Content, "@Orchestrator")

	absent := healthMonitorStore(t)
	_, err = absent.Subscribe(ctx, "absent", "Worker", "Worker", "absent-worker", nil, nil, "")
	require.NoError(t, err)
	task, err = absent.CreateTask(ctx, "absent", "Active", "", "medium", "Orchestrator", "Worker")
	require.NoError(t, err)
	_, err = absent.ClaimTask(ctx, "absent", "Worker", task.ID)
	require.NoError(t, err)
	NewBoardHealthMonitor(absent, time.Hour).scan(ctx)
	count, err := absent.CountMessages(ctx, "absent")
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestBoardHealthMonitor_FailedFindingPostRetries(t *testing.T) {
	s := healthMonitorStore(t)
	healthMonitorBoard(t, s, "retry")
	healthMonitorIssue(t, s, "retry")
	ctx := context.Background()
	m := NewBoardHealthMonitor(s, time.Hour)
	failed := true
	m.SetPostMessageFn(func(context.Context, string, string, string, *string) (*board.Message, error) {
		if failed {
			return nil, errors.New("forced post failure")
		}
		return nil, nil
	})
	m.scan(ctx)
	count, err := s.CountMessages(ctx, "retry")
	require.NoError(t, err)
	require.Zero(t, count)
	failed = false
	m.SetPostMessageFn(s.PostMessage)
	m.scan(ctx)
	count, err = s.CountMessages(ctx, "retry")
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestBoardHealthMonitor_FailedCombinedPostRetriesAllSections(t *testing.T) {
	s := healthMonitorStore(t)
	healthMonitorBoard(t, s, "combined-retry")
	ctx := context.Background()
	active, err := s.CreateTask(ctx, "combined-retry", "Idle active work", "", "medium", "Orchestrator", "Worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "combined-retry", "Worker", active.ID)
	require.NoError(t, err)
	healthMonitorIssue(t, s, "combined-retry")
	m := NewBoardHealthMonitor(s, time.Hour)
	m.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	m.SetIdleThresholds(time.Hour, 3*time.Hour)
	failed := true
	m.SetPostMessageFn(func(context.Context, string, string, string, *string) (*board.Message, error) {
		if failed {
			return nil, errors.New("forced combined post failure")
		}
		return nil, nil
	})
	m.scan(ctx)
	count, err := s.CountMessages(ctx, "combined-retry")
	require.NoError(t, err)
	require.Zero(t, count)
	failed = false
	m.SetPostMessageFn(s.PostMessage)
	m.scan(ctx)
	messages, err := s.ListMessages(ctx, "combined-retry", 100, 0, 0)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Contains(t, messages[0].Content, "unblocked tasks can potentially run in parallel")
	require.Contains(t, messages[0].Content, "[Task #")
	require.Contains(t, messages[0].Content, "active work: 1 in_progress task(s)")
}

func TestBoardHealthMonitor_NeverCapturesTerminalOutput(t *testing.T) {
	s := healthMonitorStore(t)
	healthMonitorBoard(t, s, "no-capture")
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "no-capture", "Active", "", "medium", "Orchestrator", "Worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "no-capture", "Worker", task.ID)
	require.NoError(t, err)
	probe := &healthRuntimeProbe{}
	m := NewBoardHealthMonitor(s, time.Hour)
	m.SetRuntime(probe)
	m.scan(ctx)
	require.Zero(t, probe.captureCalls)
	messages, err := s.ListMessages(ctx, "no-capture", 100, 0, 0)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.NotContains(t, messages[0].Content, "SECRET_TERMINAL_OUTPUT")
	assert.Contains(t, messages[0].Content, "Review current assignments, progress, and blockers")
}

func TestBoardHealthMonitor_IdleFindingsOnlyReachOrchestrator(t *testing.T) {
	s := healthMonitorStore(t)
	healthMonitorBoard(t, s, "idle-routing")
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "idle-routing", "Idle work", "", "medium", "Orchestrator", "Worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "idle-routing", "Worker", task.ID)
	require.NoError(t, err)
	m := NewBoardHealthMonitor(s, time.Hour)
	m.SetIdleThresholds(time.Nanosecond, time.Hour)
	m.scan(ctx)
	messages, err := s.ListMessages(ctx, "idle-routing", 100, 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, messages)
	for _, msg := range messages {
		assert.Contains(t, msg.Content, "@Orchestrator")
		assert.NotContains(t, msg.Content, "@Worker")
	}
	workerMessages, err := s.ReadMessages(ctx, "idle-routing", "Worker", 50)
	require.NoError(t, err)
	assert.Empty(t, workerMessages)
}
