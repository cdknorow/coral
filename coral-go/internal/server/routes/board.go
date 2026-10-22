package routes

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
)

const taskNudge = "You have tasks available. Run 'coral-board task claim' to start."

// A task-specific notice must claim that task, not a different FIFO candidate.
// Generic availability notices continue to use taskNudge and normal queue order.
func taskClaimNudge(taskID int64) string {
	return fmt.Sprintf("[Task #%d available] Run 'coral-board task claim %d' to claim it when your current task is done.", taskID, taskID)
}

// BoardHandler handles message board HTTP endpoints.
type BoardHandler struct {
	bs                            *board.Store
	terminal                      ptymanager.SessionTerminal
	mu                            sync.RWMutex
	paused                        map[string]bool      // in-memory set of paused project names
	notifyFn                      func(project string) // triggers an immediate notification pass for project's board
	coralDir                      string
	alsoMu                        sync.Mutex    // serialises adding a later receipt to a received row (#4449)
	autoAttach                    *AutoAttacher // #3515: an agent's linked images and Markdown
	writeTaskArtifact             func(context.Context, *board.Task, string) error
	reminderMu                    sync.Mutex
	reminders                     map[string]context.CancelFunc
	subscriberReminderIntervals   map[string]int
	subscriberReminderGenerations map[string]uint64
	// subscriberReminderFire holds one mutex per active subscriber reminder.
	// The firing goroutine holds it across its cancellation check and the
	// nudge, so stopping a reminder can wait for an in-flight nudge.
	subscriberReminderFire map[string]*sync.Mutex
	// subscriberReminderOps serializes durable-store plus timer changes for one
	// team/subscriber so concurrent replace and remove cannot interleave.
	subscriberReminderOps map[string]*sync.Mutex
	// reminderExited, when set, runs as a reminder goroutine ends, after its cleanup (tests:
	// a replaced reminder's old goroutine must not clear the new one, #3788).
	reminderExited atomic.Pointer[func()]
	// deleteSubscriberReminder is the durable delete; tests inject failures.
	deleteSubscriberReminder func(ctx context.Context, project, subscriber string) error
	// crossTeam rate-limits cross-team messages per sender and target.
	crossTeam crossTeamLimiter
	// received limits the files other Corals push (cross-team artifacts).
	received receivedLimiter
	// allowlistFlag reads the global switch that puts the cross-team allowlist in force (#5508).
	allowlistFlag func(ctx context.Context) bool
	// listEdits limits how often one owner session edits a board's list (#5508).
	listEdits editLimiter
	// hubSwitches limits Coral Hub's requests to turn a team's cross-team switches on or off (#5572); hubCrossTeamLock reads the local lock.
	hubSwitches      hubSwitchLimiter
	hubCrossTeamLock func(ctx context.Context) (bool, error)
	// links is the cross-team link table Coral Hub's snapshots fill (#5572); linksSeenRead and linksSeenWrite keep the one-way "a hub advertised links" flag; ownServerID reads this computer's
	// Coral Hub server id; linkClock is the clock (tests inject one).
	links          linkTable
	linksSeenRead  func(ctx context.Context) (bool, error)
	linksSeenWrite func(ctx context.Context) error
	ownServerID    func(ctx context.Context) (string, error)
	linkClock      func() time.Time
	// relayCoarse are the coarse limits on what one source Coral can push (#5514).
	relayCoarse relayCoarseLimits
	// relay sends a cross-team message to another Coral through Coral Hub (#3489).
	relay RelaySender
	// relayArtifact sends a file to another Coral through Coral Hub (cross-team artifacts).
	relayArtifact RelayArtifactSender
	// hubNotices drops a repeated Coral Hub notice (#3489).
	hubNotices hubNoticeSeen
	// isArchivedSession reports whether a board session (agentType-sessionID) is
	// an archived agent. Nil means no agent is ever treated as archived.
	isArchivedSession func(ctx context.Context, sessionName string) bool
	// liveSessionType is a live session's agent type by session id (ok false when it has none);
	// board notices are typed only into a live session of the type its name says (#3864).
	// Nil skips that check (a handler without the sessions database).
	liveSessionType func(ctx context.Context, sessionID string) (agentType string, ok bool, err error)
	// uploads caches per-session upload records for the artifact views.
	uploads uploadDirCache
	// hubOperatorAllowed reports whether a tunnel request may act as the Operator
	// (#3404); nil means never.
	hubOperatorAllowed func(*http.Request) bool
	// background counts the fire-and-forget work a request starts after it answers
	// (notifications, wait resolution), all of which use the store,
	// so a shutdown or a test can wait for it before closing the store (#3486).
	background sync.WaitGroup
	// taskArtifactLocks serialises the after-the-reply writes of one task's changes.diff (task id -> *sync.Mutex), so two
	// completions of the same task close together (complete then cancel, or a retry) write one after the other, never
	// interleaved. Each write is atomic already (a rename into place); this keeps a slower, older diff from landing last.
	taskArtifactLocks sync.Map
}

// SetHubOperatorAllowed tells the board whether a Coral Hub user may act as the
// Operator (CloudHandler.HubOperatorAllowed).
func (h *BoardHandler) SetHubOperatorAllowed(fn func(*http.Request) bool) {
	h.hubOperatorAllowed = fn
}

// SetArchivedSessionLookup tells the board which sessions are archived agents:
// tasks are never assigned to them and task nudges skip them.
func (h *BoardHandler) SetArchivedSessionLookup(fn func(ctx context.Context, sessionName string) bool) {
	h.isArchivedSession = fn
}

// SetLiveSessionLookup tells the board how to find a live session's agent type (#3864).
func (h *BoardHandler) SetLiveSessionLookup(fn func(ctx context.Context, sessionID string) (string, bool, error)) {
	h.liveSessionType = fn
}

// subscriberArchived reports whether subscriberID's session on project is an
// archived agent.
func (h *BoardHandler) subscriberArchived(ctx context.Context, project, subscriberID string) bool {
	if h.isArchivedSession == nil || subscriberID == "" {
		return false
	}
	sub, err := h.bs.GetProjectSubscription(ctx, project, subscriberID)
	if err != nil || sub == nil || sub.SessionName == "" {
		return false
	}
	return h.isArchivedSession(ctx, sub.SessionName)
}

// refuseArchivedAssignee answers 409 when assignee is an archived agent and
// reports whether it did.
func (h *BoardHandler) refuseArchivedAssignee(w http.ResponseWriter, ctx context.Context, project, assignee string) bool {
	if !h.subscriberArchived(ctx, project, assignee) {
		return false
	}
	writeError(w, http.StatusConflict,

		"assignee_archived", fmt.Sprintf("%s is archived; unarchive it before assigning it work", assignee),
	)
	return true
}

// SetTaskArtifactWriter configures persistence of task patches under .coral.
// SetAutoAttacher turns on auto-attach for agents' own posts (#3515).
func (h *BoardHandler) SetAutoAttacher(a *AutoAttacher) { h.autoAttach = a }

func (h *BoardHandler) SetTaskArtifactWriter(coralDir string, fn func(context.Context, *board.Task, string) error) {
	h.coralDir = coralDir
	h.writeTaskArtifact = fn
}

func (h *BoardHandler) taskArtifactPath(taskID int64) string {
	return filepath.Join(h.coralDir, "artifacts", "tasks", strconv.FormatInt(taskID, 10), "changes.diff")
}

// taskArtifactTimeout bounds the after-the-reply writing of a task's changes.diff: the agent's working tree is diffed (git diff,
// and one git diff --no-index per untracked file), which on a large tree takes many seconds.
const taskArtifactTimeout = 2 * time.Minute

// persistTaskArtifactAfterReply writes the task's changes.diff after the request has answered, on a context of its own (#217).
// It was written before the reply, on the request's context: on a large working tree that took longer than coral-board's
// 10-second client timeout, so the client reported a failure for a completion that had been committed, the closed connection
// cancelled the request's context, that killed the git it was running, and the artifact was never written ("context canceled").
// It stays best-effort, as before: a failure is logged, never returned.
func (h *BoardHandler) persistTaskArtifactAfterReply(task *board.Task) {
	if task == nil || task.SessionID == nil || *task.SessionID == "" || h.coralDir == "" || h.writeTaskArtifact == nil {
		return
	}
	lock, _ := h.taskArtifactLocks.LoadOrStore(task.ID, &sync.Mutex{})
	h.goBG(func() {
		lock.(*sync.Mutex).Lock() // one write at a time for this task
		defer lock.(*sync.Mutex).Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), taskArtifactTimeout)
		defer cancel()
		h.persistTaskArtifact(ctx, task)
	})
}

func (h *BoardHandler) persistTaskArtifact(ctx context.Context, task *board.Task) {
	if task == nil || h.coralDir == "" || h.writeTaskArtifact == nil {
		return
	}
	// A task completed by the API key, the command line or the dashboard has no agent session,
	// so no working tree to take the changes from: nothing to persist, which is normal (#4098:
	// the soak logged this as a warning for every such task).
	if task.SessionID == nil || *task.SessionID == "" {
		slog.Debug("task changes not persisted: the task has no agent session", "task_id", task.ID, "board", task.BoardID)
		return
	}
	if err := h.writeTaskArtifact(ctx, task, h.taskArtifactPath(task.ID)); err != nil {
		slog.Warn("persist task changes artifact failed", "task_id", task.ID, "board", task.BoardID, "error", err)
	}
}

func NewBoardHandler(bs *board.Store) *BoardHandler {
	return &BoardHandler{
		bs:                            bs,
		paused:                        make(map[string]bool),
		reminders:                     make(map[string]context.CancelFunc),
		subscriberReminderIntervals:   make(map[string]int),
		subscriberReminderGenerations: make(map[string]uint64),
	}
}

// SetTerminal sets the terminal backend for peek functionality.
func (h *BoardHandler) SetTerminal(t ptymanager.SessionTerminal) {
	h.terminal = t
}

// SetNotifyFn sets a callback that triggers an immediate notification pass for a board.
func (h *BoardHandler) SetNotifyFn(fn func(project string)) {
	h.notifyFn = fn
}

// ── Group endpoints ──────────────────────────────────────────────────

// ── Task endpoints ───────────────────────────────────────────────────

// goBG runs f after the request, counted in h.background (#3486).
func (h *BoardHandler) goBG(f func()) {
	h.background.Add(1)
	go func() {
		defer safego.Recover("server/routes/board:2544")
		defer h.background.Done()
		f()
	}()
}

// WaitBackground waits for the work requests started after answering (#3486):
// call it before closing the store, or that work can still open the database.
// Call it only once the server takes no more requests (after its Close or
// Shutdown): a request starting work while the wait is at zero is a WaitGroup
// misuse.
func (h *BoardHandler) WaitBackground() {
	h.background.Wait()
}

// relayCoarseLimits are the per-source-and-target and per-source limiters of relayed messages, built on first use so that they share the handler's test clock.
type relayCoarseLimits struct {
	once      sync.Once
	perTarget crossTeamLimiter
	perSource crossTeamLimiter
}
