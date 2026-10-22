package background

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// reconcileMissThreshold is the number of consecutive reconcile passes an
// awake session must be missing from the runtime before it is marked as
// sleeping. A single miss can be a transient tmux hiccup rather than a crash.
const reconcileMissThreshold = 2

// orphanGracePeriod is how long after a session is marked stopped in the DB
// the reconciler waits before killing a runtime session that is still alive
// under that ID. Gives in-flight kill/restart handlers time to finish.
const orphanGracePeriod = 2 * time.Minute

// SessionReconciler periodically checks live sessions against running
// processes and marks crashed agents as sleeping. This catches agents that
// die between server restarts — without it, they stay "live" in the DB
// forever with no running process.
//
// To avoid falsely sleeping a healthy agent, a session is only marked as
// sleeping when (a) it is absent from ListAgents, (b) a direct IsAlive probe
// on its session name also fails, and (c) that happens on
// reconcileMissThreshold consecutive passes.
type SessionReconciler struct {
	sessionStore *store.SessionStore
	runtime      AgentRuntime
	interval     time.Duration
	logger       *slog.Logger
	misses       map[string]int // session_id -> consecutive passes not found
	exited       map[string]int // session_id -> consecutive passes its terminal showed a plain shell
	now          func() time.Time
	// onSlept hears the sessions one pass marked sleeping (an exited CLI or a gone terminal),
	// for the exit notice (#3613). Unset: nothing is told.
	onSlept func(ctx context.Context, sessionIDs []string)
	slept   []string // this pass's
}

// SetOnSlept registers f to hear, once per pass, the agents that pass found dead.
func (r *SessionReconciler) SetOnSlept(f func(ctx context.Context, sessionIDs []string)) {
	r.onSlept = f
}

// exitedLaunchGrace is how long after launch a pane showing a shell is not yet
// taken as an exited agent: the CLI is typed into that shell and needs a moment
// to start.
const exitedLaunchGrace = time.Minute

// NewSessionReconciler creates a new SessionReconciler.
func NewSessionReconciler(ss *store.SessionStore, rt AgentRuntime, interval time.Duration) *SessionReconciler {
	return &SessionReconciler{
		sessionStore: ss,
		runtime:      rt,
		interval:     interval,
		logger:       slog.Default().With("service", "session_reconciler"),
		misses:       make(map[string]int),
		exited:       make(map[string]int),
		now:          time.Now,
	}
}

// Run starts the reconciliation loop.
func (r *SessionReconciler) Run(ctx context.Context) error {
	return runOnTicker(ctx, r.interval, func() { r.reconcileOnce(ctx) })
}

func (r *SessionReconciler) reconcileOnce(ctx context.Context) {
	liveSessions, err := r.sessionStore.GetAllLiveSessions(ctx)
	if err != nil {
		r.logger.Error("failed to read live sessions", "error", err)
		return
	}

	agents, err := r.runtime.ListAgents(ctx)
	if err != nil {
		r.logger.Error("failed to list running agents", "error", err)
		return
	}
	alive := make(map[string]bool, len(agents))
	for _, a := range agents {
		alive[strings.ToLower(a.SessionID)] = true
	}

	r.slept = r.slept[:0]
	defer func() {
		if len(r.slept) > 0 && r.onSlept != nil {
			r.onSlept(ctx, append([]string(nil), r.slept...))
		}
	}()

	seen := make(map[string]bool, len(liveSessions))
	for _, ls := range liveSessions {
		if ls.IsSleeping == 1 {
			delete(r.misses, ls.SessionID)
			continue
		}
		seen[ls.SessionID] = true
		if alive[strings.ToLower(ls.SessionID)] {
			delete(r.misses, ls.SessionID)
			r.checkExited(ctx, ls)
			continue
		}

		// Not in the discovered list — confirm with a direct probe before
		// trusting it. ListAgents parses tmux output and can miss a session
		// for reasons unrelated to the agent being dead.
		name := FormatSessionName(ls.AgentType, ls.SessionID)
		if r.runtime.IsAlive(ctx, name) {
			delete(r.misses, ls.SessionID)
			r.checkExited(ctx, ls)
			r.logger.Debug("agent missing from runtime listing but liveness probe succeeded",
				"session_id", ls.SessionID, "session_name", name)
			continue
		}

		r.misses[ls.SessionID]++
		if r.misses[ls.SessionID] < reconcileMissThreshold {
			r.logger.Info("agent not found, waiting for confirmation before marking as sleeping",
				"session_id", ls.SessionID, "agent_name", ls.AgentName,
				"misses", r.misses[ls.SessionID], "threshold", reconcileMissThreshold)
			continue
		}
		delete(r.misses, ls.SessionID)

		// Its terminal session is gone: asleep, and the dashboard says why (#3344).
		if err := r.sessionStore.SetSessionSleepingFor(ctx, ls.SessionID, store.SleepReasonTerminalGone); err != nil {
			r.logger.Error("failed to mark crashed agent as sleeping",
				"session_id", ls.SessionID, "agent_name", ls.AgentName, "error", err)
			continue
		}
		r.slept = append(r.slept, ls.SessionID)
		r.logger.Warn("detected crashed agent, marking as sleeping",
			"session_id", ls.SessionID, "agent_name", ls.AgentName)
	}

	// Forget miss counts for sessions that are no longer live.
	for sid := range r.misses {
		if !seen[sid] {
			delete(r.misses, sid)
		}
	}
	for sid := range r.exited {
		if !seen[sid] {
			delete(r.exited, sid)
		}
	}

	r.killOrphans(ctx, agents)
}

// killOrphans terminates runtime sessions whose DB row was marked stopped
// more than orphanGracePeriod ago. These are sessions a kill/restart missed
// (historically because the tmux pane listing was mis-parsed) and they would
// otherwise linger forever, consuming resources and confusing the sidebar.
func (r *SessionReconciler) killOrphans(ctx context.Context, agents []AgentInfo) {
	if len(agents) == 0 {
		return
	}
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, strings.ToLower(a.SessionID))
	}
	stopped, err := r.sessionStore.GetStoppedSessions(ctx, ids)
	if err != nil {
		r.logger.Error("failed to look up stopped sessions", "error", err)
		return
	}
	for _, a := range agents {
		stoppedAt, ok := stopped[strings.ToLower(a.SessionID)]
		if !ok {
			continue
		}
		if stoppedAt != "" {
			t, err := time.Parse(store.ISOFormat, stoppedAt)
			if err != nil {
				continue // unknown timestamp format — leave it alone
			}
			if time.Since(t) < orphanGracePeriod {
				continue
			}
		}
		name := FormatSessionName(a.AgentType, a.SessionID)
		if err := r.runtime.KillAgent(ctx, name); err != nil {
			r.logger.Error("failed to kill orphaned session", "session_name", name, "error", err)
			continue
		}
		r.logger.Warn("killed orphaned runtime session (marked stopped in DB but still running)",
			"session_name", name, "stopped_at", stoppedAt)
	}
}

// checkExited marks an agent sleeping once its CLI has exited and left its pane at a
// plain shell prompt (#3219): reconcileMissThreshold consecutive passes, and not in
// the first minute after launch. The shell is left as it is; Wake (or Resume) types
// the resume command into it. A 'terminal' agent, whose shell is the agent, is never
// marked.
func (r *SessionReconciler) checkExited(ctx context.Context, ls store.LiveSession) {
	name := FormatSessionName(ls.AgentType, ls.SessionID)
	exited, fg := ptymanager.AgentExited(ctx, r.runtime, name, ls.AgentType, ls.SessionID)
	if !exited {
		delete(r.exited, ls.SessionID)
		return
	}
	if created, err := time.Parse(store.ISOFormat, ls.CreatedAt); err == nil && r.now().Sub(created) < exitedLaunchGrace {
		return
	}
	r.exited[ls.SessionID]++
	if r.exited[ls.SessionID] < reconcileMissThreshold {
		return
	}
	delete(r.exited, ls.SessionID)
	if err := r.sessionStore.SetSessionSleepingFor(ctx, ls.SessionID, store.SleepReasonAgentExited); err != nil {
		r.logger.Error("failed to mark exited agent as sleeping", "session_id", ls.SessionID, "error", err)
		return
	}
	r.slept = append(r.slept, ls.SessionID)
	r.logger.Warn("agent CLI has exited, marking as sleeping (Wake resumes it)",
		"session_id", ls.SessionID, "agent_name", ls.AgentName, "foreground", fg)
}
