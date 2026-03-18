package background

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/store"
)

// AutoAcceptSessions tracks sessions with auto_accept enabled.
// Used by the events API to auto-send acceptance.
var (
	AutoAcceptSessions = make(map[string]string) // session_id -> tmux_session_name
	AutoAcceptCounts   = make(map[string]int)    // session_id -> count
	AutoAcceptLimits   = make(map[string]int)    // session_id -> max allowed
	autoAcceptMu       sync.Mutex
)

const defaultMaxAutoAccepts = 10

// ConcurrencyLimitError is returned when the max concurrent run limit is reached.
type ConcurrencyLimitError struct {
	Limit int
}

func (e *ConcurrencyLimitError) Error() string {
	return fmt.Sprintf("Concurrent task limit reached (max: %d). Try again later.", e.Limit)
}

// JobScheduler polls scheduled_jobs, fires due runs, and manages watchdog goroutines.
type JobScheduler struct {
	store          *store.ScheduleStore
	maxConcurrent  int
	interval       time.Duration
	logger         *slog.Logger
	runningMu      sync.Mutex
	running        map[int64]context.CancelFunc // run_id -> cancel func for watchdog
	launchFn       func(ctx context.Context, job store.ScheduledJob, runID int64) error
	nextFireTimeFn func(cronExpr, tz string, after time.Time) (time.Time, error)
}

// NewJobScheduler creates a new JobScheduler.
func NewJobScheduler(schedStore *store.ScheduleStore, interval time.Duration) *JobScheduler {
	maxConcurrent := 5
	if v := os.Getenv("CORAL_MAX_CONCURRENT_JOBS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxConcurrent = n
		}
	}
	return &JobScheduler{
		store:         schedStore,
		maxConcurrent: maxConcurrent,
		interval:      interval,
		logger:        slog.Default().With("service", "job_scheduler"),
		running:       make(map[int64]context.CancelFunc),
	}
}

// SetLaunchFn sets the function called to launch an agent for a job run.
func (s *JobScheduler) SetLaunchFn(fn func(ctx context.Context, job store.ScheduledJob, runID int64) error) {
	s.launchFn = fn
}

// SetNextFireTimeFn sets the cron evaluation function.
func (s *JobScheduler) SetNextFireTimeFn(fn func(cronExpr, tz string, after time.Time) (time.Time, error)) {
	s.nextFireTimeFn = fn
}

// RunningCount returns the number of active watchdog goroutines.
func (s *JobScheduler) RunningCount() int {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	return len(s.running)
}

// Run starts the scheduler loop.
func (s *JobScheduler) Run(ctx context.Context) error {
	s.logger.Info("scheduler started", "interval", s.interval, "max_concurrent", s.maxConcurrent)

	// Reap stale runs from a previous crash
	s.reapStaleRuns(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				s.logger.Error("tick error", "error", err)
			}
		}
	}
}

func (s *JobScheduler) reapStaleRuns(ctx context.Context) {
	// Mark runs stuck in pending/running past their max_duration as killed
	activeRuns, err := s.store.ListActiveRuns(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, run := range activeRuns {
		if run.StartedAt == nil {
			continue
		}
		started, err := time.Parse(time.RFC3339, *run.StartedAt)
		if err != nil {
			continue
		}
		// Look up max_duration from the job
		job, err := s.store.GetScheduledJob(ctx, run.JobID)
		if err != nil || job == nil {
			continue
		}
		maxDur := time.Duration(job.MaxDurationS) * time.Second
		if maxDur == 0 {
			maxDur = time.Hour
		}
		elapsed := now.Sub(started)
		if elapsed > maxDur*2 { // generous 2x buffer
			s.logger.Warn("reaping stale run", "run_id", run.ID, "elapsed", elapsed)
			finished := now.Format(time.RFC3339)
			s.store.UpdateScheduledRun(ctx, run.ID, map[string]interface{}{
				"status":      "killed",
				"exit_reason": "timeout_reap",
				"finished_at": finished,
			})
		}
	}
}

func (s *JobScheduler) tick(ctx context.Context) error {
	if s.nextFireTimeFn == nil {
		return nil // No cron evaluator configured
	}

	jobs, err := s.store.ListScheduledJobs(ctx, true)
	if err != nil {
		return err
	}

	nowUTC := time.Now().UTC()
	for _, job := range jobs {
		if job.Name == "__oneshot__" {
			continue
		}
		if err := s.evaluateJob(ctx, job, nowUTC); err != nil {
			s.logger.Error("job evaluation error", "job", job.Name, "error", err)
		}
	}

	// Clean up finished watchdog entries
	s.runningMu.Lock()
	for runID, cancel := range s.running {
		_ = cancel // Keep reference
		run, err := s.store.GetScheduledRun(ctx, runID)
		if err != nil || run == nil || (run.Status != "pending" && run.Status != "running") {
			delete(s.running, runID)
		}
	}
	s.runningMu.Unlock()

	return nil
}

func (s *JobScheduler) evaluateJob(ctx context.Context, job store.ScheduledJob, now time.Time) error {
	// Check if there's already an active run
	active, err := s.store.GetActiveRunForJob(ctx, job.ID)
	if err != nil {
		return err
	}
	if active != nil {
		return nil // Already running
	}

	// Check last run
	lastRun, err := s.store.GetLastRunForJob(ctx, job.ID)
	if err != nil {
		return err
	}

	// Calculate next fire time
	var afterTime time.Time
	if lastRun != nil {
		afterTime, _ = time.Parse(time.RFC3339, lastRun.ScheduledAt)
	} else {
		afterTime = now.Add(-24 * time.Hour) // Look back 24h for first run
	}

	nextFire, err := s.nextFireTimeFn(job.CronExpr, job.Timezone, afterTime)
	if err != nil {
		return fmt.Errorf("cron parse error for job %s: %w", job.Name, err)
	}

	if nextFire.After(now) {
		return nil // Not due yet
	}

	// Check concurrency limit
	s.runningMu.Lock()
	if len(s.running) >= s.maxConcurrent {
		s.runningMu.Unlock()
		return nil // At capacity
	}
	s.runningMu.Unlock()

	// Create a run record
	runID, err := s.store.CreateScheduledRun(ctx, job.ID, now.Format(time.RFC3339), "pending")
	if err != nil {
		return err
	}

	// Launch the agent (if launch function is configured)
	if s.launchFn != nil {
		go func() {
			watchCtx, cancel := context.WithTimeout(context.Background(), time.Duration(job.MaxDurationS)*time.Second)
			defer cancel()

			s.runningMu.Lock()
			s.running[runID] = cancel
			s.runningMu.Unlock()

			startedAt := time.Now().UTC().Format(time.RFC3339)
			s.store.UpdateScheduledRun(context.Background(), runID, map[string]interface{}{
				"status":     "running",
				"started_at": startedAt,
			})

			err := s.launchFn(watchCtx, job, runID)
			finishedAt := time.Now().UTC().Format(time.RFC3339)

			status := "completed"
			exitReason := "success"
			if err != nil {
				status = "failed"
				exitReason = err.Error()
			}
			if watchCtx.Err() == context.DeadlineExceeded {
				status = "killed"
				exitReason = "timeout"
			}

			s.store.UpdateScheduledRun(context.Background(), runID, map[string]interface{}{
				"status":      status,
				"finished_at": finishedAt,
				"exit_reason": exitReason,
			})

			s.runningMu.Lock()
			delete(s.running, runID)
			s.runningMu.Unlock()
		}()
	}

	return nil
}
