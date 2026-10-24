package background

import (
	"context"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/store"
)

// A scheduled prompt run is one finite invocation (#7090): its agent is given the job's prompt, answers it, and the run is over. The run finishes on the agent's own accepted end of that turn (a stop its
// hooks stored, after the prompt's turn started), not when somebody exits the CLI or the deadline arrives. Only scheduled prompt jobs of the agents that report their turns (Claude, Codex) are finished this
// way: a team's agents, a workflow's steps, a one-shot task and every other agent stay as they were.
//
// One path settles a run for every way it can end (settleOnTurn): the live watcher and the watcher of a run adopted after a restart share it, and so does the check at startup. It first claims the run in
// the store (FinishScheduledRunIfActive), so a stop, the deadline, a user's kill and a repeated event finish it once and none overwrites another.

// The exit reasons a run finished by its own turn gets: completed when the turn ended normally, failed when it ended in an error (a StopFailure). Neither is agent_done (the agent's process ended), timeout or
// user_cancelled.
const (
	exitTurnComplete = "turn_complete"
	exitTurnFailed   = "turn_failed"
	turnFailedMsg    = "the agent's turn ended with an error"
	// turnFailedCode is the fixed error_code of a run whose turn ended in an error, like the other failure reasons' (agent_exited_before_start, the sign-in codes): a script matches on it.
	turnFailedCode = "agent_turn_failed"
)

var (
	// turnPoll is how often a live run's stored events are read for the end of its turn (a variable for the tests).
	turnPoll = 5 * time.Second
	// turnSettle is how long a stop must have stood, as the newest thing the agent did, before the run is finished by it: the hook that stored it has acknowledged, and a stop hook that sends the agent on
	// has had its say (a continuation shows as new activity, which puts the turn back to working).
	turnSettle = 3 * time.Second
)

// SetSessionTurn tells the scheduler how to read how a session's current turn stands (the startup passes the task store's read). Without it no run is finished by its turn.
func (s *JobScheduler) SetSessionTurn(fn func(ctx context.Context, sessionID string) (store.RunTurn, error)) {
	s.sessionTurn = fn
}

// finishesOnTurn reports whether a run of this job is finished by its agent's accepted turn end: a scheduled prompt job (never the one-shot sentinel, never a workflow) of an agent that reports its turns.
func (s *JobScheduler) finishesOnTurn(job store.ScheduledJob) bool {
	if s.sessionTurn == nil || job.Name == "__oneshot__" || job.JobType == "workflow" {
		return false
	}
	return job.AgentType == at.Claude || job.AgentType == at.Codex || job.AgentType == ""
}

// turnEnd reads the run's own session: whether its turn has ended, normally or not, and long enough ago. A run with no session yet, a read that fails and a turn still going are not ended.
func (s *JobScheduler) turnEnd(ctx context.Context, run store.ScheduledRun, now time.Time) (failed, ended bool) {
	if run.SessionID == nil || *run.SessionID == "" {
		return false, false
	}
	turn, err := s.sessionTurn(ctx, *run.SessionID)
	if err != nil || (turn.State != store.TurnDone && turn.State != store.TurnFailed) {
		return false, false
	}
	if at, perr := parseTimestamp(turn.At); perr != nil || now.Sub(at) < turnSettle {
		return false, false
	}
	return turn.State == store.TurnFailed, true
}

// settleOnTurn finishes the run when its turn has ended, and reports whether this call did. It claims the run first: a run another path already finished (a kill, the deadline, an earlier call) is left as
// that path recorded it. The run's own agent (its session by name, with the input held for it) is then ended, the live watcher stopped, and the worktree cleanup left to the caller, which does it only when
// the agent is gone: a folder a live process still works in is never removed under it.
func (s *JobScheduler) settleOnTurn(ctx context.Context, run store.ScheduledRun, job store.ScheduledJob, failed bool) (settled, agentGone bool) {
	status, reason, msg, code := "completed", exitTurnComplete, "", ""
	if failed {
		status, reason, msg, code = "failed", exitTurnFailed, turnFailedMsg, turnFailedCode
	}
	won, err := s.store.FinishScheduledRunIfActive(ctx, run.ID, status, reason, msg, code, time.Now().UTC().Format(store.ISOFormat))
	if err != nil || !won {
		return false, false
	}
	s.logger.Info("a scheduled run finished on its agent's accepted turn end", "run_id", run.ID, "job", job.Name, "status", status)
	agentGone = s.endRunSession(ctx, run, job)
	// Recorded before the live watcher is released: the launch function returns on the cancel, and launchAndWatch must find the run already settled (and its agent already ended), not end it again.
	s.turnEnded.Store(run.ID, agentGone)
	s.runningMu.Lock()
	if cancel, ok := s.running[run.ID]; ok {
		cancel()
	}
	s.runningMu.Unlock()
	return true, agentGone
}

// endRunSession ends the agent a run started, by its own session name, drops the input held for it and reports whether it is gone. It says no when it cannot tell (no runtime) or the kill did not take.
func (s *JobScheduler) endRunSession(ctx context.Context, run store.ScheduledRun, job store.ScheduledJob) bool {
	if s.runtime == nil || run.SessionID == nil || *run.SessionID == "" {
		return false
	}
	name := s.runAgentType(ctx, run, job.AgentType) + "-" + *run.SessionID
	ptymanager.DropHeld(name, "the scheduled run finished")
	if err := s.runtime.KillAgent(ctx, name); err != nil {
		s.logger.Warn("could not end the finished run's agent", "run_id", run.ID, "session", name, "error", err)
	}
	return !s.runtime.IsAlive(ctx, name)
}

// watchTurn is the live run's check for the end of its turn: every turnPoll, until the run is over, it reads the session's stored events and settles the run when its turn has ended. It also catches a stop
// stored before it looked (the first read is not a subscription).
func (s *JobScheduler) watchTurn(ctx context.Context, runID int64, job store.ScheduledJob) {
	ticker := time.NewTicker(turnPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run, err := s.store.GetScheduledRun(ctx, runID)
			if err != nil || run == nil || (run.Status != "pending" && run.Status != "running") {
				if err == nil {
					return
				}
				continue
			}
			if failed, ended := s.turnEnd(ctx, *run, time.Now()); ended {
				s.settleOnTurn(ctx, *run, job, failed)
				return
			}
		}
	}
}

// startTurnWatch starts watchTurn for a run that finishes on its turn.
func (s *JobScheduler) startTurnWatch(ctx context.Context, runID int64, job store.ScheduledJob) {
	if !s.finishesOnTurn(job) {
		return
	}
	s.watching.Add(1)
	go func() {
		defer safego.Recover("background/scheduler:watchTurn")
		defer s.watching.Done()
		s.watchTurn(ctx, runID, job)
	}()
}

// takeTurnEnded reports, once, whether the run was settled on its turn and whether its agent was gone then (for the cleanup that follows).
func (s *JobScheduler) takeTurnEnded(runID int64) (settled, agentGone bool) {
	v, ok := s.turnEnded.LoadAndDelete(runID)
	if !ok {
		return false, false
	}
	gone, _ := v.(bool)
	return true, gone
}

// settleAdoptedOnTurn is settleOnTurn for a run found at startup or watched after one: it reads the run's stored turn now, finishes it when it has ended and removes the worktree only if the agent is gone.
// It reports whether the run was settled by this call.
func (s *JobScheduler) settleAdoptedOnTurn(ctx context.Context, run store.ScheduledRun, job store.ScheduledJob) bool {
	if !s.finishesOnTurn(job) {
		return false
	}
	failed, ended := s.turnEnd(ctx, run, time.Now())
	if !ended {
		return false
	}
	settled, gone := s.settleOnTurn(ctx, run, job, failed)
	if settled && gone {
		s.cleanupSettledWorktree(ctx, run, job)
	}
	if settled {
		s.turnEnded.Delete(run.ID) // no live watcher takes it
	}
	return settled
}
