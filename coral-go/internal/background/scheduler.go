package background

import (
	"context"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/coralnames"
	"github.com/subgentic/coral/internal/logsafe"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/store"
)

// parseTimestamp parses a timestamp string, trying store.ISOFormat (with microseconds) first,
// then falling back to time.RFC3339 for backwards compatibility.
func parseTimestamp(s string) (time.Time, error) {
	if t, err := time.Parse(store.ISOFormat, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// lookbackStart is the time after which a job's next slot counts as due. It
// starts from the last run (or 24h ago for a job that never ran), so a slot
// missed while Coral was down is caught up once. It never reaches back past
// the job's last change (updated_at: creation, an edit, or pause/resume): a
// job created at 10:00 for "weekdays 9:00" waits for tomorrow's 9:00 instead
// of firing at once, and a resumed job does not fire the slots it missed
// while paused (#3136).
func lookbackStart(job store.ScheduledJob, lastRun *store.ScheduledRun, now time.Time) time.Time {
	after := now.Add(-24 * time.Hour)
	if lastRun != nil {
		if t, err := parseTimestamp(lastRun.ScheduledAt); err == nil {
			after = t
		}
	}
	changed := job.UpdatedAt
	if changed == "" {
		changed = job.CreatedAt
	}
	if t, err := parseTimestamp(changed); err == nil && t.After(after) {
		after = t
	}
	return after
}

// jobFailurePauseAfter is how many failed runs in a row pause a job.
const jobFailurePauseAfter = 3

// pauseIfFailing pauses job when its last jobFailurePauseAfter runs all
// failed, recording why (the newest error) so the Jobs view can show it; the
// job then waits until someone fixes and resumes it (#3171). Reports whether
// it paused the job.
func (s *JobScheduler) pauseIfFailing(ctx context.Context, job store.ScheduledJob) bool {
	runs, err := s.store.GetRunsForJob(ctx, job.ID, jobFailurePauseAfter)
	if err != nil || len(runs) < jobFailurePauseAfter {
		return false
	}
	resumed, hasResume := time.Time{}, false
	if job.ResumedAt != "" {
		if t, err := parseTimestamp(job.ResumedAt); err == nil {
			resumed, hasResume = t, true
		}
	}
	for _, r := range runs {
		if r.Status != "failed" {
			return false
		}
		// A run refused because the agent CLI was signed out says nothing about the job: it does not count toward the pause, and the job runs again once signed in (#5469).
		if r.ErrorCode != nil && store.IsSignInRefusalCode(*r.ErrorCode) {
			return false
		}
		// A run from before the job was resumed does not count: the user fixed
		// the cause and resumed it, so it gets its runs again (#3171 review).
		if hasResume {
			if t, err := parseTimestamp(r.ScheduledAt); err != nil || !t.After(resumed) {
				return false
			}
		}
	}
	reason := fmt.Sprintf("Paused after %d failed runs in a row", jobFailurePauseAfter)
	if e := runs[0].ErrorMsg; e != nil && strings.TrimSpace(*e) != "" {
		reason += ": " + strings.TrimSpace(*e)
	}
	if _, err := s.store.UpdateScheduledJob(ctx, job.ID, map[string]interface{}{"enabled": 0, "paused_reason": reason}); err != nil {
		return false
	}
	s.logger.Warn("job paused after repeated failures", "job", job.Name, "reason", reason)
	return true
}

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
	sessionStore   *store.SessionStore
	lastEventType  func(ctx context.Context, sessionID string) (last string, stopFailed bool) // the type of the agent's last event (stop, tool_use...), "" for none, and whether a stop was a failed turn (an API error); nil: not known (#6898)
	hasEvents      func(ctx context.Context, sessionID string) bool                           // whether the agent's CLI reported anything (its hook events); nil: a run is never judged by it (#6842)
	runtime        AgentRuntime
	maxConcurrent  int
	interval       time.Duration
	logger         *slog.Logger
	parentCtx      context.Context // set by Run(); used by launchAndWatch for shutdown
	runningMu      sync.Mutex
	running        map[int64]context.CancelFunc // run_id -> cancel func for watchdog
	watching       sync.WaitGroup               // the launchAndWatch goroutines
	launchFn       func(ctx context.Context, job store.ScheduledJob, runID int64) error
	nextFireTimeFn func(cronExpr, tz string, after time.Time) (time.Time, error)
	worktreeRoot   string // where run worktrees go (Coral's data dir); "" = beside the repo (legacy)
	workflowRunner *WorkflowRunner
	workflowStore  *store.WorkflowStore
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

// SetSessionStore sets the session store used for tagging sessions.
func (s *JobScheduler) SetSessionStore(ss *store.SessionStore) {
	s.sessionStore = ss
}

// SetRuntime sets the agent runtime used for killing sessions.
func (s *JobScheduler) SetRuntime(rt AgentRuntime) {
	s.runtime = rt
}

// SetWorkflowRunner sets the workflow runner and store for workflow-type jobs.
func (s *JobScheduler) SetWorkflowRunner(wr *WorkflowRunner, ws *store.WorkflowStore) {
	s.workflowRunner = wr
	s.workflowStore = ws
}

// SetWorktreeRoot sets the directory run worktrees are created under (the
// "worktrees" directory in Coral's data dir), instead of beside the user's
// repository (#3171).
func (s *JobScheduler) SetWorktreeRoot(dir string) {
	s.worktreeRoot = dir
}

// runWorktreeDir is where run runID's worktree of repoPath goes:
// <worktreeRoot>/job-run-<id>-<repo name>, or beside the repository when no
// root is set.
func (s *JobScheduler) runWorktreeDir(repoPath string, runID int64) string {
	if s.worktreeRoot != "" {
		_ = os.MkdirAll(s.worktreeRoot, 0o755)
	}
	return coralnames.JobRunWorktree(s.worktreeRoot, repoPath, runID)
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
	s.parentCtx = ctx // store for launchAndWatch goroutines
	s.logger.Info("scheduler started", "interval", s.interval, "max_concurrent", s.maxConcurrent)

	// Reap stale runs from a previous crash
	s.reapStaleRuns(ctx)

	return runOnTicker(ctx, s.interval, func() {
		if err := s.tick(ctx); err != nil {
			s.logger.Error("tick error", "error", err)
		}
	})
}

// adoptedRunPoll is how often a run adopted at startup is checked for its agent having ended (a variable for the tests).
var adoptedRunPoll = 30 * time.Second

// reapStaleRuns settles the runs a previous Coral left in pending or running (#6898). A run is advanced and watched only by in-process goroutines, so a restart leaves each of them behind, and the next
// slots of its job are skipped as "already running" until something finishes it. At startup, for each such run:
//   - one older than twice its job's longest allowed run is killed as before (timeout_reap, and its agent with it);
//   - one that never launched an agent (no session) is failed: Coral restarted before its agent existed;
//   - one whose agent has ended while Coral was down is completed (agent_done);
//   - one whose agent is still alive is adopted: watched again until the agent ends (completed) or the run's time is up (killed, timeout), as an in-process run is.
//
// What an adopted run does not redo is the work only its first goroutine held: removing its worktree, the auto-accept registration, an agent-exit judgement.
func (s *JobScheduler) reapStaleRuns(ctx context.Context) {
	activeRuns, err := s.store.ListActiveRuns(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, run := range activeRuns {
		job, err := s.store.GetScheduledJob(ctx, run.JobID)
		if err != nil || job == nil {
			continue
		}
		maxDur := time.Duration(job.MaxDurationS) * time.Second
		if maxDur == 0 {
			maxDur = time.Hour
		}
		hasSession := run.SessionID != nil && *run.SessionID != ""
		var started time.Time
		hasStart := false
		if run.StartedAt != nil {
			if t, perr := parseTimestamp(*run.StartedAt); perr == nil {
				started, hasStart = t, true
			}
		}
		finish := func(status, reason, msg string) {
			fields := map[string]interface{}{"status": status, "exit_reason": reason, "finished_at": now.Format(store.ISOFormat)}
			if msg != "" {
				fields["error_msg"] = msg
			}
			s.store.UpdateScheduledRun(ctx, run.ID, fields)
		}
		switch {
		case hasStart && now.Sub(started) > maxDur*2: // generous 2x buffer
			s.logger.Warn("reaping stale run", "run_id", run.ID, "elapsed", now.Sub(started))
			s.killRunSession(ctx, &run, s.runAgentType(ctx, run, job.AgentType))
			finish("killed", "timeout_reap", "")
			s.cleanupSettledWorktree(ctx, run, *job)
		case !hasSession:
			s.logger.Warn("a run left over from before Coral restarted never launched its agent", "run_id", run.ID)
			finish("failed", "interrupted_by_restart", "Coral restarted before this run launched its agent")
		case !hasStart:
			continue
		case !s.agentAlive(ctx, run, job.AgentType):
			s.logger.Info("a run's agent ended while Coral was down", "run_id", run.ID)
			// "agent_done" only when its last event shows it finished: it may have crashed while Coral was down, and the run history must not claim a success it never saw.
			reason := "agent_done"
			if s.lastEventType != nil {
				if last, stopFailed := s.lastEventType(ctx, *run.SessionID); (last != "stop" && last != "session_end") || stopFailed { // a stop that is a failed turn (StopFailure: an API error) is not a finish
					reason = "ended_while_coral_was_down"
				}
			}
			finish("completed", reason, "")
			s.cleanupSettledWorktree(ctx, run, *job)
		default:
			s.adoptRun(ctx, run, *job, started.Add(maxDur))
		}
	}
}

// cleanupSettledWorktree removes the worktree of a run settled or adopted at startup, as a run that ended in-process has its removed: by the same no-force rule (a worktree with unsaved work stays, with its branch),
// and only for a job that asks for cleanup and only the folder the scheduler makes for that run (IsRunWorktreeFolder: never the job's own repository). A one-shot's cleanup flag is not stored, so a one-shot
// run's worktree stays (and shows in the kept-worktrees list). Without it a job every 5 minutes, restarted once, would leave one stale worktree.
func (s *JobScheduler) cleanupSettledWorktree(ctx context.Context, run store.ScheduledRun, job store.ScheduledJob) {
	fresh, err := s.store.GetScheduledRun(ctx, run.ID)
	if err != nil || fresh == nil || fresh.WorktreePath == nil || job.CleanupWorktree != 1 || job.RepoPath == "" {
		return
	}
	path := *fresh.WorktreePath
	if !coralnames.IsJobRunWorktree(s.worktreeRoot, job.RepoPath, run.ID, path) {
		return
	}
	base, err := gitutil.ResolveBase(ctx, job.RepoPath, job.BaseBranch)
	if err != nil {
		base = "" // the run's branch is then kept: it cannot be compared with a base
	}
	s.cleanupWorktree(job.RepoPath, path, coralnames.JobRunBranch(run.ID), base)
}

// agentAlive reports whether the run's agent session still exists; with no runtime to ask it is taken as alive (the run is then watched, not finished on a guess).
func (s *JobScheduler) agentAlive(ctx context.Context, run store.ScheduledRun, agentType string) bool {
	if s.runtime == nil || run.SessionID == nil {
		return true
	}
	return s.runtime.IsAlive(ctx, s.runAgentType(ctx, run, agentType)+"-"+*run.SessionID)
}

// runAgentType is the agent type the run's session was started as: the type on its session row, else the job's (else Claude). A job's agent type edited since the run started would otherwise name a session that
// never existed, and a live agent would be read as gone (Security's LOW on #6904).
func (s *JobScheduler) runAgentType(ctx context.Context, run store.ScheduledRun, jobType string) string {
	if s.sessionStore != nil && run.SessionID != nil {
		if ls, err := s.sessionStore.GetLiveSession(ctx, *run.SessionID); err == nil && ls != nil && ls.AgentType != "" {
			return ls.AgentType
		}
	}
	if jobType == "" {
		return at.Claude
	}
	return jobType
}

// adoptRun watches a run whose agent outlived the previous Coral, as launchAndWatch's launch function does for a run it started: until the agent ends (completed) or deadline passes (killed, timeout). A shutdown
// leaves the run as it is for the next start to settle again.
func (s *JobScheduler) adoptRun(ctx context.Context, run store.ScheduledRun, job store.ScheduledJob, deadline time.Time) {
	s.logger.Info("adopting a run whose agent outlived the previous Coral", "run_id", run.ID, "until", deadline.Format(time.RFC3339))
	s.watching.Add(1)
	go func() {
		defer safego.Recover("background/scheduler:adoptRun")
		defer s.watching.Done()
		ticker := time.NewTicker(adoptedRunPoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !s.agentAlive(ctx, run, job.AgentType) {
					s.finishRun(ctx, run.ID, job, "completed", "agent_done", false)
					s.cleanupSettledWorktree(ctx, run, job)
					return
				}
				if time.Now().After(deadline) {
					s.finishRun(ctx, run.ID, job, "killed", "timeout", false) // finishRun ends its agent
					s.cleanupSettledWorktree(ctx, run, job)
					return
				}
			}
		}
	}()
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

	// A job that keeps failing pauses itself, with the reason shown (#3171).
	if lastRun != nil && lastRun.Status == "failed" && s.pauseIfFailing(ctx, job) {
		return nil
	}

	// Calculate next fire time
	afterTime := lookbackStart(job, lastRun, now)

	nextFire, err := s.nextFireTimeFn(job.CronExpr, job.Timezone, afterTime)
	if err != nil {
		return fmt.Errorf("cron parse error for job %s: %w", job.Name, err)
	}

	if nextFire.After(now) {
		return nil // Not due yet
	}

	// Check concurrency limit and pre-register to prevent TOCTOU race
	s.runningMu.Lock()
	if len(s.running) >= s.maxConcurrent {
		s.runningMu.Unlock()
		return nil // At capacity
	}

	// Create a run record while holding the lock
	runID, err := s.store.CreateScheduledRun(ctx, job.ID, now.Format(store.ISOFormat), "pending")
	if err != nil {
		s.runningMu.Unlock()
		return err
	}

	// Pre-register placeholder so concurrent evaluateJob calls see it
	s.running[runID] = func() {}
	s.runningMu.Unlock()

	// Dispatch based on job type
	if job.JobType == "workflow" && job.WorkflowID != nil {
		go func() {
			defer safego.Recover("background/scheduler:s.triggerWorkflowForJob")
			s.triggerWorkflowForJob(runID, job)
		}()
	} else if s.launchFn != nil {
		rc := runConfig{
			repoPath:        job.RepoPath,
			baseBranch:      job.BaseBranch,
			maxDurationS:    job.MaxDurationS,
			cleanupWorktree: job.CleanupWorktree == 1,
			createWorktree:  true,
			tag:             "scheduled",
		}
		s.watch(runID, job, rc)
	}

	return nil
}

// triggerWorkflowForJob triggers a workflow run for a scheduled workflow job,
// then updates the scheduled run record when the workflow completes.
func (s *JobScheduler) triggerWorkflowForJob(runID int64, job store.ScheduledJob) {
	ctx := s.parentCtx
	if ctx == nil {
		ctx = context.Background()
	}

	s.runningMu.Lock()
	runCtx, cancel := context.WithCancel(ctx)
	s.running[runID] = cancel
	s.runningMu.Unlock()

	defer func() {
		s.runningMu.Lock()
		delete(s.running, runID)
		s.runningMu.Unlock()
		cancel()
	}()

	// Look up the workflow
	if s.workflowRunner == nil || s.workflowStore == nil {
		s.logger.Error("workflow runner not configured", "job", job.Name)
		s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
			"status": "failed", "error_msg": "workflow runner not configured",
			"finished_at": time.Now().UTC().Format(store.ISOFormat),
		})
		return
	}

	wf, err := s.workflowStore.GetWorkflow(ctx, *job.WorkflowID)
	if err != nil || wf == nil {
		s.logger.Error("workflow not found", "workflow_id", *job.WorkflowID, "error", err)
		s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
			"status": "failed", "error_msg": "workflow not found",
			"finished_at": time.Now().UTC().Format(store.ISOFormat),
		})
		return
	}

	// Mark scheduled run as running
	s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
		"status": "running", "started_at": time.Now().UTC().Format(store.ISOFormat),
	})

	// Trigger the workflow
	triggerCtx := fmt.Sprintf(`{"scheduled_job_id":%d,"scheduled_run_id":%d}`, job.ID, runID)
	wfRun, err := s.workflowRunner.TriggerWorkflow(runCtx, wf, "scheduled", &triggerCtx)
	if err != nil {
		s.logger.Error("failed to trigger workflow", "job", job.Name, "error", err)
		s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
			"status": "failed", "error_msg": err.Error(),
			"finished_at": time.Now().UTC().Format(store.ISOFormat),
		})
		return
	}

	// Poll for workflow run completion
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-runCtx.Done():
			s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
				"status": "failed", "error_msg": "cancelled",
				"finished_at": time.Now().UTC().Format(store.ISOFormat),
			})
			return
		case <-ticker.C:
			run, err := s.workflowRunner.store.GetWorkflowRunDirect(context.Background(), wfRun.ID)
			if err != nil {
				continue
			}
			if run == nil {
				continue
			}
			switch run.Status {
			case "completed":
				s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
					"status":      "completed",
					"finished_at": time.Now().UTC().Format(store.ISOFormat),
				})
				return
			case "failed", "killed":
				errMsg := "workflow " + run.Status
				if run.ErrorMsg != nil {
					errMsg = *run.ErrorMsg
				}
				fields := map[string]interface{}{
					"status": run.Status, "error_msg": errMsg,
					"finished_at": time.Now().UTC().Format(store.ISOFormat),
				}
				if run.ErrorCode != nil { // the workflow run's fixed code and CLI (#5469) are the scheduled run's too
					fields["error_code"] = *run.ErrorCode
				}
				if run.AgentType != nil {
					fields["agent_type"] = *run.AgentType
				}
				s.store.UpdateScheduledRun(ctx, runID, fields)
				return
			}
			// Still pending/running — continue polling
		}
	}
}

// runConfig holds launch parameters for a run.
type runConfig struct {
	repoPath        string
	baseBranch      string
	maxDurationS    int
	cleanupWorktree bool
	createWorktree  bool
	tag             string // "scheduled" or "task"
}

// FireOneshot submits a one-shot task run from the API.
// Returns the run_id. Returns ConcurrencyLimitError if at capacity.
func (s *JobScheduler) FireOneshot(ctx context.Context, config map[string]interface{}) (int64, error) {
	s.runningMu.Lock()
	if len(s.running) >= s.maxConcurrent {
		s.runningMu.Unlock()
		return 0, &ConcurrencyLimitError{Limit: s.maxConcurrent}
	}
	s.runningMu.Unlock()

	now := time.Now().UTC().Format(store.ISOFormat)
	var displayName *string
	if v, ok := config["display_name"].(string); ok && v != "" {
		displayName = &v
	}

	runID, err := s.store.CreateOneshotRun(ctx, now, displayName)
	if err != nil {
		return 0, err
	}

	repoPath, _ := config["repo_path"].(string)
	baseBranch, _ := config["base_branch"].(string) // empty: the repository's current branch, chosen when the run's worktree is made (#6701)
	maxDur, _ := config["max_duration_s"].(float64)
	if maxDur == 0 {
		maxDur = 3600
	}
	createWT, _ := config["create_worktree"].(bool)
	cleanupWT, _ := config["cleanup_worktree"].(bool)

	// Build a synthetic ScheduledJob for the launch function
	prompt, _ := config["prompt"].(string)
	agentType, _ := config["agent_type"].(string)
	if agentType == "" {
		agentType = at.Claude
	}
	flags, _ := config["flags"].(string)
	// Not "Task #N": board tasks are numbered #N too (#3171).
	dn := fmt.Sprintf("Job run %d", runID)
	if displayName != nil {
		dn = *displayName
	}

	job := store.ScheduledJob{
		RepoPath:   repoPath,
		BaseBranch: baseBranch,
		Prompt:     prompt,
		AgentType:  agentType,
		Flags:      flags,
		Name:       dn,
	}

	rc := runConfig{
		repoPath:        repoPath,
		baseBranch:      baseBranch,
		maxDurationS:    int(maxDur),
		cleanupWorktree: cleanupWT,
		createWorktree:  createWT,
		tag:             "task",
	}

	s.watch(runID, job, rc)
	return runID, nil
}

// KillRun kills a running task by run_id. Returns true if killed.
func (s *JobScheduler) KillRun(ctx context.Context, runID int64) bool {
	run, err := s.store.GetScheduledRun(ctx, runID)
	if err != nil || run == nil || (run.Status != "pending" && run.Status != "running") {
		return false
	}

	// Kill the agent session if we have a session_id
	agentType := at.Claude
	if job, err := s.store.GetScheduledJob(ctx, run.JobID); err == nil && job != nil {
		agentType = job.AgentType
	}
	s.killRunSession(ctx, run, agentType)

	now := time.Now().UTC().Format(store.ISOFormat)
	s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
		"status":      "killed",
		"exit_reason": "user_cancelled",
		"finished_at": now,
	})

	// Cancel watchdog goroutine if tracked
	s.runningMu.Lock()
	if cancel, ok := s.running[runID]; ok {
		cancel()
		delete(s.running, runID)
	}
	s.runningMu.Unlock()

	return true
}

// killRunSession kills the agent session a run launched, if it recorded one.
func (s *JobScheduler) killRunSession(ctx context.Context, run *store.ScheduledRun, agentType string) {
	if run == nil || run.SessionID == nil || *run.SessionID == "" || s.runtime == nil {
		return
	}
	if agentType == "" {
		agentType = at.Claude
	}
	name := agentType + "-" + *run.SessionID
	if err := s.runtime.KillAgent(ctx, name); err != nil {
		s.logger.Warn("could not kill the run's agent", "run_id", run.ID, "session", name, "error", err)
	}
}

// launchAndWatch is the core goroutine that creates worktrees, launches agents,
// monitors via watchdog, fires webhooks, and cleans up.
// watch runs launchAndWatch on its own goroutine, counted so waitWatchers can wait for it.
func (s *JobScheduler) watch(runID int64, job store.ScheduledJob, rc runConfig) {
	s.watching.Add(1)
	go func() {
		defer safego.Recover("background/scheduler:609")
		defer s.watching.Done()
		s.launchAndWatch(runID, job, rc)
	}()
}

// waitWatchers waits until every run's launchAndWatch has returned, its final status
// written (tests: a kill's own status must still stand once the watchdog is done, #3788).
func (s *JobScheduler) waitWatchers() { s.watching.Wait() }

// shuttingDown reports whether Coral is stopping: the scheduler's context is done. A run's own deadline and a user's kill cancel the run's context, not this one.
func (s *JobScheduler) shuttingDown() bool { return s.parentCtx != nil && s.parentCtx.Err() != nil }

// WaitWatchers is the shutdown's wait for the run goroutines to return, so the process does not exit while one is still running its end-of-run code. It waits at most d, and only once the scheduler's context is
// done (while Coral runs on, runs go on and there is nothing to wait for). It reports false only when it gave up with a run goroutine still going.
func (s *JobScheduler) WaitWatchers(d time.Duration) bool {
	if s.parentCtx == nil || s.parentCtx.Err() == nil {
		return true
	}
	done := make(chan struct{})
	safego.Go("background/scheduler:WaitWatchers", func() { s.watching.Wait(); close(done) })
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func (s *JobScheduler) launchAndWatch(runID int64, job store.ScheduledJob, rc runConfig) {
	// Use the scheduler's parent context so goroutines cancel on shutdown.
	// Falls back to Background if Run() hasn't been called (e.g., FireOneshot).
	ctx := s.parentCtx
	if ctx == nil {
		ctx = context.Background()
	}
	maxDur := rc.maxDurationS
	if maxDur == 0 {
		maxDur = 3600
	}

	watchCtx, cancel := context.WithTimeout(ctx, time.Duration(maxDur)*time.Second)
	defer cancel()

	s.runningMu.Lock()
	s.running[runID] = cancel
	s.runningMu.Unlock()

	defer func() {
		s.runningMu.Lock()
		delete(s.running, runID)
		s.runningMu.Unlock()
	}()

	// A scheduled run of a signed-out agent CLI is refused before anything is made for it (#5469): no worktree, no launch, and no sign-in flow in a tmux pane nobody watches.
	if rc.tag == "scheduled" && s.refuseIfNotSignedIn(ctx, runID, job) {
		return
	}

	wt, ok := s.prepareRunWorktree(ctx, watchCtx, runID, rc)
	if !ok {
		return
	}

	// Update run with worktree path
	startedAt := time.Now().UTC().Format(store.ISOFormat)
	s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
		"status":        "running",
		"started_at":    startedAt,
		"worktree_path": wt.workingDir,
	})

	// Launch the agent via the configured launch function
	var launchErr error
	if s.launchFn != nil {
		// Pass the working dir via a modified job
		jobCopy := job
		jobCopy.RepoPath = wt.workingDir
		launchErr = s.launchFn(watchCtx, jobCopy, runID)
	}
	if s.shuttingDown() { // Coral is shutting down, not the run ending (its own deadline and a user's kill cancel watchCtx, not this): the agent outlives Coral and is still working in its worktree, so nothing is finished, removed or written, and the next start settles or adopts the run (#6918, #6911)
		return
	}
	if launchErr == nil {
		s.afterLaunch(ctx, runID, job, rc)
	}

	status, exitReason := runOutcome(launchErr, watchCtx.Err())
	if status == "failed" && errors.Is(launchErr, ptymanager.ErrStartCommand) { // one plain sentence and a fixed code, not "spawn agent failed: ..." (#6978)
		exitReason = ptymanager.ErrStartCommand.Error()
		s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{"error_msg": exitReason, "error_code": startCommandFailedCode})
	}
	if status == "completed" && s.exitedBeforeStarting(ctx, runID, job, time.Now().UTC().Format(store.ISOFormat), startedAt) {
		status, exitReason = "failed", agentExitedEarlyMsg
	}
	s.finishRun(ctx, runID, job, status, exitReason, launchErr != nil)

	// Cleanup worktree if configured
	if rc.cleanupWorktree && wt.worktreeDir != "" {
		s.cleanupWorktree(rc.repoPath, wt.worktreeDir, wt.runBranch, wt.baseBranch)
	}
}

// SetEventCheck tells the scheduler how to ask whether an agent's CLI reported anything (a stored hook event), the signal that it started (the startup passes the task store's read).
func (s *JobScheduler) SetEventCheck(fn func(ctx context.Context, sessionID string) bool) {
	s.hasEvents = fn
}

// SetLastEventType tells the scheduler how to read the type of an agent's last stored event and whether it was a failed stop (the startup passes the task store's reads): a run whose agent ended while Coral was down
// is recorded finished only when that event shows the agent ended its work (a stop that did not fail, session_end).
func (s *JobScheduler) SetLastEventType(fn func(ctx context.Context, sessionID string) (last string, stopFailed bool)) {
	s.lastEventType = fn
}

// startCommandFailedCode is the run's fixed error_code when Coral could not hand the agent its start command (#6978).
const startCommandFailedCode = "start_command_failed"

// agentExitedEarlyWindow is how short a run must be, from its start to the agent's exit, to be judged by whether the agent ever reported anything.
var agentExitedEarlyWindow = 15 * time.Second

const (
	agentExitedEarlyMsg  = "the agent exited before it started: it ended within seconds without reporting anything"
	agentExitedEarlyCode = "agent_exited_before_start"
)

// exitedBeforeStarting reports whether a run that ended on its own did so before its agent started: within agentExitedEarlyWindow of the run's start, and its session never reported an event (Claude and Codex
// report through hooks from their first moment; a one-shot that ends fast on purpose has reported events by then). It records the plain reason and the fixed error_code on the run when it says yes. The runtime
// gives no exit code, so none is recorded. A scheduler with no event check, a CLI that reports no events (any other agent type), a run with no session, or a read that fails is never judged: it stays completed.
func (s *JobScheduler) exitedBeforeStarting(ctx context.Context, runID int64, job store.ScheduledJob, nowISO, startedISO string) bool {
	if s.hasEvents == nil || (job.AgentType != "claude" && job.AgentType != "codex" && job.AgentType != "") {
		return false
	}
	started, err1 := time.Parse(store.ISOFormat, startedISO)
	now, err2 := time.Parse(store.ISOFormat, nowISO)
	if err1 != nil || err2 != nil || now.Sub(started) > agentExitedEarlyWindow {
		return false
	}
	run, err := s.store.GetScheduledRun(ctx, runID)
	if err != nil || run == nil || run.SessionID == nil || *run.SessionID == "" || run.Status == "killed" {
		return false
	}
	if s.hasEvents(ctx, *run.SessionID) {
		return false
	}
	s.logger.Warn("scheduled run: the agent exited before it started", "run_id", runID, "job", job.Name)
	s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{"error_msg": agentExitedEarlyMsg, "error_code": agentExitedEarlyCode})
	return true
}

// refuseIfNotSignedIn fails the scheduled run at once, with the sentence, the fixed error_code and the CLI named, when the job's agent CLI has a status command and is signed out. It
// reports whether it did. A CLI that cannot be found or asked does not stop the run here.
func (s *JobScheduler) refuseIfNotSignedIn(ctx context.Context, runID int64, job store.ScheduledJob) bool {
	if s.sessionStore == nil || !agentsignin.Supports(job.AgentType) {
		return false
	}
	settings, err := s.sessionStore.GetSettingsForCLI(ctx)
	if err != nil {
		return false
	}
	bin, err := agent.FindCLI(settings, job.AgentType)
	if err != nil {
		return false
	}
	var nsi *agentsignin.NotSignedInError
	if err := agentsignin.RequireFreshIn(ctx, job.AgentType, bin, job.RepoPath); !errors.As(err, &nsi) {
		return false
	}
	s.logger.Warn("scheduled run not started: "+nsi.Error(), "job", job.Name, "run_id", runID)
	s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
		"status": "failed", "error_msg": nsi.Error(), "exit_reason": nsi.Error(),
		"error_code": nsi.Code(), "agent_type": nsi.Agent,
		"finished_at": time.Now().UTC().Format(store.ISOFormat),
	})
	return true
}

// runWorktree is where a run's agent works: the repository, or a worktree made for the run on its own branch.
type runWorktree struct {
	workingDir, worktreeDir, runBranch, baseBranch string
}

// prepareRunWorktree makes the run's worktree when the job asks for one, branching coral/job-run-<id> from the base
// branch (main by default): git refuses to check out a branch that is already checked out in another worktree, and
// the user's own working copy almost always has the base branch checked out, which is exactly what our docs give as
// the default, so checking out the base directly failed on every run of a correctly configured job. The worktree
// is keyed on the run, and the branch the same way, so each run stays isolated. A base branch refused (a stored job
// is checked again, #3601 weak site W24) or a worktree that can't be made fails the run (ok false).
func (s *JobScheduler) prepareRunWorktree(ctx, watchCtx context.Context, runID int64, rc runConfig) (runWorktree, bool) {
	wt := runWorktree{workingDir: rc.repoPath}
	if !rc.createWorktree || rc.repoPath == "" {
		return wt, true
	}
	wt.worktreeDir = s.runWorktreeDir(rc.repoPath, runID)
	wt.baseBranch = rc.baseBranch
	wt.runBranch = coralnames.JobRunBranch(runID)
	// The base branch the job names, else the repository's current branch: a job stored with the old default "main" runs in a repository on master too (#6701).
	var err error
	errMsg := ""
	errCode := "worktree_failed" // the fixed word a row and the Runs table show (#6701)
	if wt.baseBranch, err = gitutil.ResolveBase(watchCtx, rc.repoPath, wt.baseBranch); err != nil {
		var missing *gitutil.BranchMissingError
		if errors.As(err, &missing) {
			errMsg, errCode = missing.Error(), "base_branch_missing" // one plain sentence, no git text
		} else {
			errMsg = fmt.Sprintf("git worktree add failed: %v", err)
		}
	} else if err = gitutil.ValidRef(wt.baseBranch); err != nil {
		errMsg = fmt.Sprintf("base_branch refused: %v", err)
	} else if existing := s.runWorktreeExisting(watchCtx, rc.repoPath, wt.worktreeDir, wt.runBranch); existing != "" { // older leftovers of this id are never touched, nor rolled back over (#6927)
		err = errors.New(existing)
		errMsg = existing
	} else if err = runGitCmd(watchCtx, rc.repoPath, "-c", "core.longpaths=true", "worktree", "add", "-b", wt.runBranch, "--", wt.worktreeDir, wt.baseBranch); err != nil {
		errMsg = fmt.Sprintf("git worktree add failed: %v", err)
		s.rollBackWorktreeAdd(rc.repoPath, wt.worktreeDir, wt.runBranch, runID)
	}
	if err != nil {
		if s.shuttingDown() { // the shutdown ended the add, not a failure of the run's: not written as failed, the next start settles the row (#6927)
			return wt, false
		}
		s.logger.Error(errMsg, "run_id", runID)
		now := time.Now().UTC().Format(store.ISOFormat)
		s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
			"status":      "failed",
			"error_msg":   errMsg,
			"error_code":  errCode,
			"finished_at": now,
		})
		return wt, false
	}
	wt.workingDir = wt.worktreeDir
	return wt, true
}

// runWorktreeExisting says, in one plain sentence, whether the folder or the branch this run is about to make is already there ("" when neither is): a run id that repeats finds an older run's. The add is then not
// attempted, so the rollback only ever removes what this run's add made.
func (s *JobScheduler) runWorktreeExisting(ctx context.Context, repoPath, dir, branch string) string {
	if _, err := os.Lstat(dir); err == nil {
		return "the run's worktree folder already exists"
	}
	// Only git's clean "not found" (exit status 1 with --quiet) says the branch is not there: any other failure (a cancelled context, a broken repository) is unknown, and the add is refused (#6936).
	err := gitutil.Command(ctx, repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return "the run's branch already exists"
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return ""
	default:
		return "git could not tell whether the run's branch already exists"
	}
}

// rollBackWorktreeAdd leaves no folder, registration or branch of a worktree add that failed (#6717). It runs on a context of its own: a shutdown that ended the add (killing git halfway, folder and registration made)
// cancels the run's context, and a rollback on it would do nothing (#6927). The folder is removed only when it is the one the scheduler makes for this run.
func (s *JobScheduler) rollBackWorktreeAdd(repoPath, dir, branch string, runID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if coralnames.IsJobRunWorktree(s.worktreeRoot, repoPath, runID, dir) {
		err := os.RemoveAll(dir) // a half-made checkout git was killed in
		if err != nil {          // on Windows a git killed halfway may hold files for a moment
			time.Sleep(300 * time.Millisecond)
			if err = os.RemoveAll(dir); err != nil {
				s.logger.Warn("could not remove the half-made worktree folder", "path", logsafe.Dir(dir), "error", err)
			}
		}
	}
	runGitCmd(ctx, repoPath, "worktree", "prune")
	runGitCmd(ctx, repoPath, "branch", "-D", "--", branch)
}

// afterLaunch tags the launched session (e.g. "scheduled" or "task").
func (s *JobScheduler) afterLaunch(ctx context.Context, runID int64, job store.ScheduledJob, rc runConfig) {
	run, err := s.store.GetScheduledRun(ctx, runID)
	if err != nil || run == nil || run.SessionID == nil {
		return
	}
	if rc.tag != "" {
		s.tagSession(ctx, *run.SessionID, rc.tag)
	}
}

// runOutcome is a run's final status and reason: completed when the agent is done, failed with the launch's error,
// killed when it ran past its time.
func runOutcome(launchErr, watchErr error) (status, exitReason string) {
	status, exitReason = "completed", "agent_done"
	if launchErr != nil {
		status, exitReason = "failed", launchErr.Error()
	}
	if watchErr == context.DeadlineExceeded {
		status, exitReason = "killed", "timeout"
	}
	return status, exitReason
}

// finishRun records the run's outcome. A run that timed out or failed after launching must not leave its agent
// running (audit HIGH-02): its session is killed before the worktree is removed from under it. A user's kill
// (KillRun) has already killed it and recorded the kill, which the "failed" its cancel produces must not overwrite;
// a Coral shutdown cancels ctx itself, and agents outlive a restart, so neither kills here.
func (s *JobScheduler) finishRun(ctx context.Context, runID int64, job store.ScheduledJob, status, exitReason string, launchFailed bool) {
	now := time.Now().UTC().Format(store.ISOFormat)
	userKilled := false
	if run, err := s.store.GetScheduledRun(ctx, runID); err == nil && run != nil {
		userKilled = run.Status == "killed"
		if !userKilled && ctx.Err() == nil && (status == "killed" || launchFailed) {
			s.killRunSession(ctx, run, s.runAgentType(ctx, *run, job.AgentType))
		}
	}
	if !userKilled {
		s.store.UpdateScheduledRun(ctx, runID, map[string]interface{}{
			"status":      status,
			"finished_at": now,
			"exit_reason": exitReason,
		})
	}
}

// tagSession ensures a tag exists and applies it to a session.
func (s *JobScheduler) tagSession(ctx context.Context, sessionID, tagName string) {
	if s.sessionStore == nil || tagName == "" {
		return
	}
	tags, err := s.sessionStore.ListTags(ctx)
	if err != nil {
		s.logger.Warn("failed to list tags for session tagging", "error", err)
		return
	}
	var tagID int64
	for _, t := range tags {
		if t.Name == tagName {
			tagID = t.ID
			break
		}
	}
	if tagID == 0 {
		// Create the tag with a default color
		color := "#f78166" // orange, matches Python default
		tag, err := s.sessionStore.CreateTag(ctx, tagName, color)
		if err != nil {
			s.logger.Warn("failed to create tag", "tag", tagName, "error", err)
			return
		}
		tagID = tag.ID
	}
	if err := s.sessionStore.AddSessionTag(ctx, sessionID, tagID); err != nil {
		s.logger.Warn("failed to tag session", "session_id", sessionID, "tag", tagName, "error", err)
	}
}

// createWorktree creates a git worktree for an isolated job run.
func runGitCmd(ctx context.Context, repoPath string, args ...string) error {
	cmd := gitutil.Command(ctx, repoPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(out))
	}
	return nil
}

// cleanupWorktree removes a job run's worktree, and its per-run branch when
// that branch holds nothing.
//
// The branch is only deleted when it has no commits the base does not already
// have. Removing a worktree discards uncommitted work by design, but deleting
// a branch the agent committed to would make finished work unreachable, so a
// run that produced commits leaves its branch behind for the user to find.
func (s *JobScheduler) cleanupWorktree(repoPath, worktreePath, runBranch, baseBranch string) {
	if filepath.Clean(worktreePath) == filepath.Clean(repoPath) { // never the job's own repository, which may be a person's checkout (#6736)
		return
	}
	if s.shuttingDown() { // its own context outlives a shutdown: a removal reached while Coral stops would take a folder a live agent works in (#6918, #6927)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := runGitCmd(ctx, repoPath, "worktree", "remove", "--", worktreePath) // no --force: git keeps a worktree with unsaved work, and so does the branch (#6701)
	if err != nil {
		s.logger.Warn("kept the run's worktree: git would not remove it", "path", logsafe.Dir(worktreePath), "error", err)
		return
	}
	s.logger.Info("cleaned up worktree", "path", logsafe.Dir(worktreePath))

	if runBranch == "" || baseBranch == "" {
		return
	}
	if branchHasCommits(ctx, repoPath, baseBranch, runBranch) {
		s.logger.Info("keeping job run branch — it has commits",
			"branch", runBranch, "base", baseBranch)
		return
	}
	if err := runGitCmd(ctx, repoPath, "branch", "-D", runBranch); err != nil {
		s.logger.Warn("failed to delete job run branch", "branch", runBranch, "error", err)
	}
}

// branchHasCommits reports whether branch contains commits that base does not.
// On any error it answers true, so an unreadable repository leaves the branch
// alone rather than risking the deletion of real work.
func branchHasCommits(ctx context.Context, repoPath, base, branch string) bool {
	if gitutil.ValidRef(base) != nil || gitutil.ValidRef(branch) != nil {
		return true // never pass git an option; an unreadable answer keeps the branch
	}
	out, err := gitutil.Command(ctx, repoPath,
		"rev-list", "--count", base+".."+branch, "--").Output()
	if err != nil {
		return true
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return true
	}
	return n > 0
}
