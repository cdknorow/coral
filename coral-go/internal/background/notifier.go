package background

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/ptymanager"
)

// BoardNotifier nudges agents when they have unread board messages.
type BoardNotifier struct {
	boardStore  *board.Store
	runtime     AgentRuntime
	interval    time.Duration
	logger      *slog.Logger
	discoverFn  func(ctx context.Context) ([]AgentInfo, error)
	isPausedFn  func(project string) bool
	notifiedMu  sync.Mutex
	notified    map[string]notifyState // session_name -> last nudge since the agent last read
	notifyNowCh chan struct{}
	pendingMu   sync.Mutex
	pendingAll  bool             // a pass over every board is due (NotifyNow)
	pending     map[string]bool  // boards a message was posted to since the last pass (NotifyBoard)
	remindAfter time.Duration    // resend when still unread this long after a nudge
	now         func() time.Time // for tests

	discMu     sync.Mutex
	discAgents []AgentInfo     // the last discovery, reused by event passes for discoveryMaxAge
	discAt     time.Time       // when it ran (zero: never)
	discAbsent map[string]bool // board subscribers a fresh discovery did not find, since the last timer pass
}

// discoveryMaxAge is how long an event pass (a post's) reuses the last agent discovery (#4299):
// a discovery is a tmux list-panes, and running one per post cost 37 a minute at 20 agents and
// 1.3 posts a second (#4275); with posts that steady, a 2 s window still ran one every 2 s. A
// stale one is safe: a session it lacks rediscovers, and so does a nudge that fails. The timer's
// pass always discovers afresh.
const discoveryMaxAge = 10 * time.Second

// notifyState records the nudge sent since the subscriber last read the board.
type notifyState struct {
	count    int
	latestID int64
	at       time.Time
}

// defaultRemindAfter is how long unread messages sit after a nudge before
// the agent is reminded. A nudge per new message piles up in a busy agent's
// input queue, so there is one nudge per batch (until the agent reads), plus
// this reminder in case the nudge was lost (typed while the agent was
// compacting, or unread messages carried across a Coral restart).
const defaultRemindAfter = 15 * time.Minute

// NewBoardNotifier creates a new BoardNotifier.
func NewBoardNotifier(boardStore *board.Store, runtime AgentRuntime, interval time.Duration) *BoardNotifier {
	return &BoardNotifier{
		boardStore:  boardStore,
		runtime:     runtime,
		interval:    interval,
		logger:      slog.Default().With("service", "board_notifier"),
		notified:    make(map[string]notifyState),
		notifyNowCh: make(chan struct{}, 1),
		remindAfter: defaultRemindAfter,
		now:         time.Now,
	}
}

// SetDiscoverFn sets a custom agent discovery function.
func (n *BoardNotifier) SetDiscoverFn(fn func(ctx context.Context) ([]AgentInfo, error)) {
	n.discoverFn = fn
}

// SetIsPausedFn sets a function to check if a board project is paused.
func (n *BoardNotifier) SetIsPausedFn(fn func(project string) bool) {
	n.isPausedFn = fn
}

// SeedFromDB populates the notified map with the current unread count and
// eligible-message watermark from the database.
// Call once during startup before Run() to avoid re-notifying agents about
// pre-existing unread messages after a server restart; they get the regular
// reminder if the messages are still unread remindAfter later.
func (n *BoardNotifier) SeedFromDB(ctx context.Context) {
	states, err := n.seedStates(ctx)
	if err != nil {
		n.logger.Warn("failed to seed notifier state", "error", err)
		return
	}
	n.notifiedMu.Lock()
	defer n.notifiedMu.Unlock()
	now := n.now()
	for sessName, state := range states {
		n.notified[sessName] = notifyState{count: state.Count, latestID: state.LatestID, at: now}
	}
	n.logger.Info("seeded notifier from DB", "sessions", len(states))
}

// seedStates are the unread states of the agents running now (#3875): the passes only nudge
// those, and seeding every subscriber a board ever had made the first nudge wait minutes on a
// long board. Without a discovery function, every subscriber.
func (n *BoardNotifier) seedStates(ctx context.Context) (map[string]board.UnreadState, error) {
	if n.discoverFn == nil {
		return n.boardStore.GetAllUnreadStates(ctx)
	}
	agents, err := n.discoverFn(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.SessionID != "" && a.AgentType != "terminal" {
			names = append(names, naming.SessionName(a.AgentType, a.SessionID))
		}
	}
	return n.boardStore.GetUnreadStatesFor(ctx, names)
}

// NotifyNow triggers an immediate notification pass over every board without waiting for the
// next tick. Safe to call from any goroutine. Non-blocking if a notification is already pending.
func (n *BoardNotifier) NotifyNow() {
	n.pendingMu.Lock()
	n.pendingAll = true
	n.pendingMu.Unlock()
	n.signal()
}

// NotifyBoard triggers an immediate pass over the agents on project's board only: a message
// posted there changes no other board's unread counts. A pass per post over every agent made
// the notifier's work grow with posts times agents (#4028 follow-up: 0.50 s, 1.18 s and 3.64 s
// of CPU per 2 minutes at 20, 40 and 80 agents). Boards posted to meanwhile share one pass; an
// empty project is NotifyNow.
func (n *BoardNotifier) NotifyBoard(project string) {
	if project == "" {
		n.NotifyNow()
		return
	}
	n.pendingMu.Lock()
	if n.pending == nil {
		n.pending = map[string]bool{}
	}
	n.pending[project] = true
	n.pendingMu.Unlock()
	n.signal()
}

// takePending returns what the next event pass covers: nil for every board, else the boards.
func (n *BoardNotifier) takePending() map[string]bool {
	n.pendingMu.Lock()
	defer n.pendingMu.Unlock()
	boards, all := n.pending, n.pendingAll
	n.pending, n.pendingAll = nil, false
	if all || len(boards) == 0 {
		return nil
	}
	return boards
}

func (n *BoardNotifier) signal() {
	select {
	case n.notifyNowCh <- struct{}{}:
	default:
		// Already a pending notification, skip
	}
}

// Run starts the notification loop.
func (n *BoardNotifier) Run(ctx context.Context) error {
	ticker := time.NewTicker(n.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := n.RunOnce(ctx); err != nil {
				n.logger.Error("notification error", "error", err)
			}
		case <-n.notifyNowCh:
			if err := n.eventPass(ctx); err != nil {
				n.logger.Error("notification error (event-driven)", "error", err)
			}
		}
	}
}

// RunOnce performs a single notification pass over every board.
func (n *BoardNotifier) RunOnce(ctx context.Context) error {
	return n.runPass(ctx, nil)
}

// eventPass is the pass a post (or NotifyNow) asks for: over what is pending, reusing a
// discovery up to discoveryMaxAge old.
func (n *BoardNotifier) eventPass(ctx context.Context) error {
	return n.runPassWith(ctx, n.takePending(), true)
}

// discover returns the running agents: the last discovery when reuse is set and it is younger
// than discoveryMaxAge (cached reports that), else a fresh one, which becomes the last.
func (n *BoardNotifier) discover(ctx context.Context, reuse bool) (agents []AgentInfo, cached bool, err error) {
	n.discMu.Lock()
	if reuse && !n.discAt.IsZero() && n.now().Sub(n.discAt) < discoveryMaxAge {
		agents = n.discAgents
		n.discMu.Unlock()
		return agents, true, nil
	}
	n.discMu.Unlock()
	agents, err = n.discoverFn(ctx)
	if err != nil {
		return nil, false, err
	}
	n.discMu.Lock()
	n.discAgents, n.discAt = agents, n.now()
	n.discMu.Unlock()
	return agents, false, nil
}

// runPass is a notification pass over the agents on boards (every board when nil) with a fresh
// discovery.
func (n *BoardNotifier) runPass(ctx context.Context, boards map[string]bool) error {
	return n.runPassWith(ctx, boards, false)
}

// runPassWith is a notification pass over the agents on boards (every board when nil). A scoped
// pass skips the agents not subscribed to one of those boards before any per-agent lookup, and
// leaves the nudge records alone: only a full pass sees every live session. With reuse, the
// agents may come from a discovery up to discoveryMaxAge old; a subscriber of those boards it
// lacks (an agent just launched) or a nudge that fails (a pane just gone) discovers again, once.
func (n *BoardNotifier) runPassWith(ctx context.Context, boards map[string]bool, reuse bool) error {
	var onBoards map[string]bool // session names subscribed to boards
	if boards != nil {
		onBoards = map[string]bool{}
		for project := range boards {
			subs, err := n.boardStore.ListSubscribers(ctx, project)
			if err != nil {
				n.logger.Warn("list subscribers failed; a full pass instead", "project", project, "error", err)
				onBoards = nil
				break
			}
			for _, sub := range subs {
				if sub.SessionName != "" {
					onBoards[sub.SessionName] = true
				}
			}
		}
	}

	agents, cached, err := n.discover(ctx, reuse)
	if err != nil {
		n.logger.Error("discover agents failed", "error", err)
		return err
	}
	if !reuse && boards == nil {
		n.discMu.Lock()
		n.discAbsent = nil
		n.discMu.Unlock()
	}
	if cached && n.lacksSubscriber(agents, onBoards) {
		if agents, cached, err = n.discover(ctx, false); err != nil {
			n.logger.Error("discover agents failed", "error", err)
			return err
		}
	}
	if !cached && onBoards != nil {
		n.noteAbsent(agents, onBoards)
	}
	// rediscover is a fresh discovery's session names, taken at most once, after a nudge from a
	// reused discovery failed.
	var fresh map[string]bool
	rediscover := func(sessName string) bool {
		if fresh == nil {
			fresh = map[string]bool{}
			again, _, err := n.discover(ctx, false)
			if err != nil {
				n.logger.Warn("discover agents failed after a failed nudge", "error", err)
			}
			for _, a := range again {
				fresh[naming.SessionName(a.AgentType, a.SessionID)] = true
			}
		}
		return fresh[sessName]
	}

	n.logger.Debug("notification pass", "agent_count", len(agents))

	liveSessions := make(map[string]bool)

	// Every agent's subscription and unread count in one batch (#4121): one query for the
	// subscriptions and one read per board, instead of several statements per agent. A failed
	// batch falls back to the lookups one agent at a time.
	var names []string
	for _, a := range agents {
		if a.SessionID == "" || a.AgentType == "terminal" {
			continue
		}
		if sn := naming.SessionName(a.AgentType, a.SessionID); onBoards == nil || onBoards[sn] {
			names = append(names, sn)
		}
	}
	batch, batchErr := n.boardStore.UnreadForSessions(ctx, names)
	if batchErr != nil {
		n.logger.Warn("batched unread counts failed; counting one agent at a time", "error", batchErr)
		batch = nil
	}

	for _, a := range agents {
		if a.SessionID == "" {
			n.logger.Debug("skipping agent with no session ID", "agent", a.AgentName)
			continue
		}
		// Terminals don't process board messages — skip them
		if a.AgentType == "terminal" {
			continue
		}

		sessName := naming.SessionName(a.AgentType, a.SessionID)
		liveSessions[sessName] = true
		if onBoards != nil && !onBoards[sessName] {
			continue
		}
		subscriberID := naming.SubscriberID(a.DisplayName, a.AgentType)

		// A discovered session must have an authoritative subscription binding.
		// Do not fall back to subscriber_id: legacy rows may have an empty or
		// stale session_name, and inferring ownership from a display name can
		// deliver another agent's unread messages.
		var sub *board.Subscriber
		var batched *board.SessionUnread
		if batch != nil {
			if b, ok := batch[sessName]; ok {
				sub, batched = b.Sub, &b
			}
			err = nil
		} else {
			sub, err = n.boardStore.GetSubscriptionBySessionName(ctx, sessName)
		}
		if err != nil {
			n.logger.Warn("subscription lookup failed", "subscriber_id", subscriberID, "session_name", sessName, "error", err)
			continue
		}
		if sub == nil {
			// Normal for sessions that never joined a board.
			n.logger.Debug("no subscription found", "subscriber_id", subscriberID, "session_name", sessName)
			continue
		}
		// The session-name lookup is authoritative across restarts. Discovery
		// display names can lag replacement metadata, so never use the derived
		// identity for unread queries after an exact subscription match.
		subscriberID = sub.SubscriberID
		// Skip remote subscribers
		if sub.OriginServer != nil && *sub.OriginServer != "" {
			n.logger.Debug("skipping remote subscriber", "subscriber_id", subscriberID)
			continue
		}
		// Skip agents on paused/sleeping boards
		if n.isPausedFn != nil && sub.Project != "" && n.isPausedFn(sub.Project) {
			n.logger.Debug("skipping paused board", "subscriber_id", subscriberID, "project", sub.Project)
			continue
		}

		var unread int
		if batched != nil {
			unread, err = batched.Unread, batched.Err
		} else {
			unread, err = n.boardStore.CheckUnread(ctx, sub.Project, subscriberID)
		}
		if err != nil {
			n.logger.Warn("check unread failed", "subscriber_id", subscriberID, "error", err)
			continue
		}

		n.logger.Debug("unread check", "subscriber_id", subscriberID, "session", sessName, "unread", unread, "project", sub.Project)

		if unread == 0 {
			n.notifiedMu.Lock()
			delete(n.notified, sessName)
			n.notifiedMu.Unlock()
			// The agent read what a held nudge for this board announces (a nudge that
			// arrives mid-turn waits for the turn's end): retire it, so it is never
			// typed into an agent with nothing to read (#3482).
			rctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			key := "board-unread:" + sub.Project
			ptymanager.TakeHeld(rctx, sessName, "read already", func(k string) bool { return k == key })
			cancel()
			continue
		}
		latestID, err := n.boardStore.LatestUnreadMessageID(ctx, sub.Project, subscriberID)
		if err != nil {
			n.logger.Warn("latest unread lookup failed", "subscriber_id", subscriberID, "error", err)
			continue
		}

		// One nudge until the agent reads; more messages arriving meanwhile
		// are covered by it (the agent reads them all at once).
		n.notifiedMu.Lock()
		last, notified := n.notified[sessName]
		n.notifiedMu.Unlock()
		// A larger unread batch means new board activity arrived. Notify
		// immediately; only suppress repeated scans of the same batch during
		// the reminder window.
		// The all-messages subscription (used by the orchestrator) needs an
		// identity watermark: after it reads batch A, batch B can arrive with
		// the same unread count before the next pass. Worker subscriptions use
		// mention-based coalescing and retain their one-nudge-per-batch policy.
		newBatch := sub.ReceiveMode == "all" && last.latestID > 0 && latestID > last.latestID
		// Any mode: the agent has read past the batch it was nudged for, so every unread
		// message is newer than that nudge. Without this a new mention that arrived before
		// a pass saw unread == 0 waited for the reminder, up to remindAfter (#3480).
		if last.latestID > 0 && sub.LastReadID >= last.latestID && latestID > last.latestID {
			newBatch = true
		}
		// The reminder window is spread per agent (#3613), so reminders set in one pass, or
		// seeded at one start, don't all come due together.
		if notified && !newBatch && (unread <= last.count || sub.ReceiveMode != "all") && n.now().Sub(last.at) < ptymanager.JitterAround(n.remindAfter, sessName) {
			n.logger.Debug("already notified", "subscriber_id", subscriberID, "session", sessName, "unread", unread, "notified_unread", last.count)
			continue
		}

		// Send nudge via tmux session name (routing identity, not board identity)
		plural := "s"
		if unread == 1 {
			plural = ""
		}
		health, healthErr := n.boardStore.HasUnreadHealthMessage(ctx, sub.Project, subscriberID)
		if healthErr != nil {
			n.logger.Warn("health message classification failed", "subscriber_id", subscriberID, "error", healthErr)
		}
		isOrchestrator := sub.CanPeek != 0 || strings.EqualFold(strings.TrimSpace(sub.JobTitle), "Orchestrator")
		if health {
			nonHealth, nonHealthErr := n.boardStore.HasUnreadNonHealthMessage(ctx, sub.Project, subscriberID)
			if nonHealthErr != nil {
				n.logger.Warn("non-health message classification failed", "subscriber_id", subscriberID, "error", nonHealthErr)
			}
			if nonHealth {
				health = false
			} else if !isOrchestrator {
				n.logger.Debug("suppressing health-only nudge for non-orchestrator", "subscriber_id", subscriberID, "session", sessName)
				continue
			}
		}
		nudge := fmt.Sprintf("You have %d unread message%s on the message board. Run 'coral-board read' to see them.", unread, plural)
		if health {
			nudge = fmt.Sprintf("[Coral health check] Team health check for %s: %d unread message%s include a health-monitor summary. Review team status, assignments, progress, and blockers, then run 'coral-board read' to see the authoritative full content.", sub.Project, unread, plural)
		}
		// A nudge about a message that tags the agent is a mention: typed in mid-turn when the
		// gate follows the signals; a routine one waits for the turn to end (#3578). Decided
		// from the unread messages themselves (confirmed tags) for every subscriber, never from
		// the receive mode alone. A failed check counts as a mention (today's behaviour).
		mention, mErr := n.boardStore.HasUnreadMention(ctx, sub.Project, subscriberID)
		if mErr != nil {
			n.logger.Warn("mention check failed; the nudge goes as a mention", "subscriber_id", subscriberID, "error", mErr)
			mention = true
		}
		n.logger.Info("sending nudge", "subscriber_id", subscriberID, "session", sessName, "unread", unread, "mention", mention)
		// One line (#3376), through the automated-input gate (#3431): a newer
		// unread count for this board replaces a held one.
		skipped, err := deliverAutomated(ctx, n.runtime, n.logger, sessName, a.AgentType, a.SessionID, "board-unread:"+sub.Project, board.OneLine(nudge), "board nudge", false, mention)
		if err != nil && cached && !skipped {
			// The discovery was reused: if the session is still running, its pane may only
			// have moved; send once more to the same session (never another).
			if !rediscover(sessName) {
				n.logger.Info("agent gone since the last discovery; no nudge", "session", sessName)
				continue
			}
			skipped, err = deliverAutomated(ctx, n.runtime, n.logger, sessName, a.AgentType, a.SessionID, "board-unread:"+sub.Project, board.OneLine(nudge), "board nudge", false, mention)
		}
		if skipped {
			continue
		}
		if err != nil {
			n.logger.Warn("failed to nudge agent", "agent", a.AgentName, "session", sessName, "error", err)
			continue
		}

		n.notifiedMu.Lock()
		n.notified[sessName] = notifyState{count: unread, latestID: latestID, at: n.now()}
		n.notifiedMu.Unlock()
	}

	if onBoards != nil {
		return nil
	}
	// Clean up stale entries
	n.notifiedMu.Lock()
	for sname := range n.notified {
		if !liveSessions[sname] {
			delete(n.notified, sname)
		}
	}
	n.notifiedMu.Unlock()

	return nil
}

// lacksSubscriber reports whether a session subscribed to the pass's boards is missing from
// agents and was not already missing from a fresh discovery since the last timer pass: an
// agent launched after agents was discovered (launch-team subscribes it first).
func (n *BoardNotifier) lacksSubscriber(agents []AgentInfo, onBoards map[string]bool) bool {
	if len(onBoards) == 0 {
		return false
	}
	running := make(map[string]bool, len(agents))
	for _, a := range agents {
		running[naming.SessionName(a.AgentType, a.SessionID)] = true
	}
	n.discMu.Lock()
	defer n.discMu.Unlock()
	for sn := range onBoards {
		if !running[sn] && !n.discAbsent[sn] {
			return true
		}
	}
	return false
}

// noteAbsent records the subscribers of the pass's boards a fresh discovery did not find (ended
// sessions keep their subscriptions), so they don't make every event pass discover again.
func (n *BoardNotifier) noteAbsent(agents []AgentInfo, onBoards map[string]bool) {
	running := make(map[string]bool, len(agents))
	for _, a := range agents {
		running[naming.SessionName(a.AgentType, a.SessionID)] = true
	}
	n.discMu.Lock()
	defer n.discMu.Unlock()
	for sn := range onBoards {
		if !running[sn] {
			if n.discAbsent == nil {
				n.discAbsent = map[string]bool{}
			}
			n.discAbsent[sn] = true
		}
	}
}
