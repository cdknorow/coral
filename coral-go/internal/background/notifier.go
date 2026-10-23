package background

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
)

// BoardNotifier nudges agents when they have unread board messages.
type BoardNotifier struct {
	// deliverNudge, when set, delivers this notifier's nudges instead of deliverNudgeFn (a test that compares two notifiers gives each its own
	// recorder without swapping the package variable, so such tests can run in parallel, #4982).
	deliverNudge nudgeDeliverFn
	boardStore   *board.Store
	runtime      AgentRuntime
	interval     time.Duration
	logger       *slog.Logger
	discoverFn   func(ctx context.Context) ([]AgentInfo, error)
	isPausedFn   func(project string) bool
	notifiedMu   sync.Mutex
	notified     map[string]notifyState // session_name -> last nudge since the agent last read
	notifyNowCh  chan struct{}
	pendingMu    sync.Mutex
	pendingAll   bool             // a pass over every board is due (NotifyNow)
	pending      map[string]bool  // boards a message was posted to since the last pass (NotifyBoard)
	remindAfter  time.Duration    // resend when still unread this long after a nudge
	now          func() time.Time // for tests

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

// watchResets forgets what a session was told when its agent's process is replaced: DropHeld discards a
// nudge held for it, and a nudge typed to the old process is gone with it, yet notified would hold the
// next nudge back until the reminder window (#6544: a Sleep/Wake lost a held nudge for up to 15 minutes).
// The next pass nudges what is still unread. It returns the function that stops watching.
func (n *BoardNotifier) watchResets() (stop func()) {
	return ptymanager.OnHeldDropped(func(name, _ string) {
		n.notifiedMu.Lock()
		delete(n.notified, name)
		n.notifiedMu.Unlock()
	})
}

// Run starts the notification loop.
func (n *BoardNotifier) Run(ctx context.Context) error {
	defer n.watchResets()()
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

// notifyPass is one pass's shared state: the agents' batched subscriptions and unread counts, whether the discovery
// was reused, and the fresh discovery a failed nudge from a reused one takes (at most once).
type notifyPass struct {
	batch  map[string]board.SessionUnread // nil: look each agent up
	cached bool
	fresh  map[string]bool
}

// runPassWith is a notification pass over the agents on boards (every board when nil). A scoped
// pass skips the agents not subscribed to one of those boards before any per-agent lookup, and
// leaves the nudge records alone: only a full pass sees every live session. With reuse, the
// agents may come from a discovery up to discoveryMaxAge old; a subscriber of those boards it
// lacks (an agent just launched) or a nudge that fails (a pane just gone) discovers again, once.
func (n *BoardNotifier) runPassWith(ctx context.Context, boards map[string]bool, reuse bool) error {
	onBoards := n.subscribersOf(ctx, boards)
	agents, cached, err := n.passAgents(ctx, boards, onBoards, reuse)
	if err != nil {
		return err
	}
	n.logger.Debug("notification pass", "agent_count", len(agents))

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
	pass := &notifyPass{batch: batch, cached: cached}

	liveSessions := make(map[string]bool)
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
		n.nudgeAgent(ctx, pass, a, sessName)
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

// subscribersOf is the session names subscribed to boards: nil for every board (boards nil), or when a board's
// subscribers can't be read (then the pass is a full one).
func (n *BoardNotifier) subscribersOf(ctx context.Context, boards map[string]bool) map[string]bool {
	if boards == nil {
		return nil
	}
	onBoards := map[string]bool{}
	for project := range boards {
		subs, err := n.boardStore.ListSubscribers(ctx, project)
		if err != nil {
			n.logger.Warn("list subscribers failed; a full pass instead", "project", project, "error", err)
			return nil
		}
		for _, sub := range subs {
			if sub.SessionName != "" {
				onBoards[sub.SessionName] = true
			}
		}
	}
	return onBoards
}

// passAgents is the pass's running agents: a reused discovery when reuse allows and it lacks no subscriber of the
// pass's boards, else a fresh one. A full timer pass forgets the subscribers absent before; a fresh scoped one
// notes those it lacks.
func (n *BoardNotifier) passAgents(ctx context.Context, boards, onBoards map[string]bool, reuse bool) ([]AgentInfo, bool, error) {
	agents, cached, err := n.discover(ctx, reuse)
	if err != nil {
		n.logger.Error("discover agents failed", "error", err)
		return nil, false, err
	}
	if !reuse && boards == nil {
		n.discMu.Lock()
		n.discAbsent = nil
		n.discMu.Unlock()
	}
	if cached && n.lacksSubscriber(agents, onBoards) {
		if agents, cached, err = n.discover(ctx, false); err != nil {
			n.logger.Error("discover agents failed", "error", err)
			return nil, false, err
		}
	}
	if !cached && onBoards != nil {
		n.noteAbsent(agents, onBoards)
	}
	return agents, cached, nil
}

// rediscover reports whether sessName is running, by a fresh discovery taken at most once per pass, after a nudge
// from a reused discovery failed.
func (n *BoardNotifier) rediscover(ctx context.Context, pass *notifyPass, sessName string) bool {
	if pass.fresh == nil {
		pass.fresh = map[string]bool{}
		again, _, err := n.discover(ctx, false)
		if err != nil {
			n.logger.Warn("discover agents failed after a failed nudge", "error", err)
		}
		for _, a := range again {
			pass.fresh[naming.SessionName(a.AgentType, a.SessionID)] = true
		}
	}
	return pass.fresh[sessName]
}

// nudgeAgent nudges one running agent when it has unread messages on its board and a nudge is due: its
// subscription (by session name only), its unread count (a read board retires a held nudge), whether a nudge is
// due (nudgeDue), what kind (nudgeKind), then the nudge through the automated-input gate, recorded when sent.
func (n *BoardNotifier) nudgeAgent(ctx context.Context, pass *notifyPass, a AgentInfo, sessName string) {
	subscriberID := naming.SubscriberID(a.DisplayName, a.AgentType)

	// A discovered session must have an authoritative subscription binding.
	// Do not fall back to subscriber_id: legacy rows may have an empty or
	// stale session_name, and inferring ownership from a display name can
	// deliver another agent's unread messages.
	var sub *board.Subscriber
	var batched *board.SessionUnread
	var err error
	if pass.batch != nil {
		if b, ok := pass.batch[sessName]; ok {
			sub, batched = b.Sub, &b
		}
	} else {
		sub, err = n.boardStore.GetSubscriptionBySessionName(ctx, sessName)
	}
	if err != nil {
		n.logger.Warn("subscription lookup failed", "subscriber_id", subscriberID, "session_name", sessName, "error", err)
		return
	}
	if sub == nil {
		// Normal for sessions that never joined a board.
		n.logger.Debug("no subscription found", "subscriber_id", subscriberID, "session_name", sessName)
		return
	}
	// The session-name lookup is authoritative across restarts. Discovery
	// display names can lag replacement metadata, so never use the derived
	// identity for unread queries after an exact subscription match.
	subscriberID = sub.SubscriberID
	// Skip remote subscribers
	if sub.OriginServer != nil && *sub.OriginServer != "" {
		n.logger.Debug("skipping remote subscriber", "subscriber_id", subscriberID)
		return
	}
	// Skip agents on paused/sleeping boards
	if n.isPausedFn != nil && sub.Project != "" && n.isPausedFn(sub.Project) {
		n.logger.Debug("skipping paused board", "subscriber_id", subscriberID, "project", sub.Project)
		return
	}

	var unread int
	if batched != nil {
		unread, err = batched.Unread, batched.Err
	} else {
		unread, err = n.boardStore.CheckUnread(ctx, sub.Project, subscriberID)
	}
	if err != nil {
		n.logger.Warn("check unread failed", "subscriber_id", subscriberID, "error", err)
		return
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
		return
	}
	latestID, err := n.boardStore.LatestUnreadMessageID(ctx, sub.Project, subscriberID)
	if err != nil {
		n.logger.Warn("latest unread lookup failed", "subscriber_id", subscriberID, "error", err)
		return
	}
	n.notifiedMu.Lock()
	last, notified := n.notified[sessName]
	n.notifiedMu.Unlock()
	if !nudgeDue(last, notified, sub, unread, latestID, n.now(), ptymanager.JitterAround(n.remindAfter, sessName)) {
		n.logger.Debug("already notified", "subscriber_id", subscriberID, "session", sessName, "unread", unread, "notified_unread", last.count)
		return
	}

	health, send := n.nudgeKind(ctx, sub, subscriberID, sessName)
	if !send {
		return
	}
	nudge := nudgeText(sub.Project, unread, health)
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
	// Counted again just before it is typed, held or not (#4362): a nudge held while the agent
	// works must not be typed after the turn in which it read those messages. A count that
	// fails keeps today's behaviour (the nudge goes).
	project, reader := sub.Project, subscriberID
	stillUnread := func(ctx context.Context) bool {
		n, err := n.boardStore.CheckUnread(ctx, project, reader)
		return err != nil || n > 0
	}
	deliver := func() (bool, error) {
		return n.nudgeDeliverer()(ctx, n.runtime, n.logger, sessName, a.AgentType, a.SessionID, "board-unread:"+sub.Project, board.OneLine(nudge), "board nudge", false, mention, stillUnread)
	}
	skipped, err := deliver()
	if err != nil && pass.cached && !skipped {
		// The discovery was reused: if the session is still running, its pane may only
		// have moved; send once more to the same session (never another).
		if !n.rediscover(ctx, pass, sessName) {
			n.logger.Info("agent gone since the last discovery; no nudge", "session", sessName)
			return
		}
		skipped, err = deliver()
	}
	if skipped {
		return
	}
	if err != nil {
		n.logger.Warn("failed to nudge agent", "agent", a.AgentName, "session", sessName, "error", err)
		return
	}
	n.notifiedMu.Lock()
	n.notified[sessName] = notifyState{count: unread, latestID: latestID, at: n.now()}
	n.notifiedMu.Unlock()
}

// nudgeDeliverFn is the type of deliverAutomatedIfWanted, which delivers a nudge.
type nudgeDeliverFn = func(ctx context.Context, rt AgentRuntime, logger *slog.Logger, name, agentType, sessionID, key, text, what string, requireInputBox, mention bool, wanted func(context.Context) bool) (skipped bool, err error)

// deliverNudgeFn delivers a nudge (tests replace it, to see each nudge and to fail some).
var deliverNudgeFn nudgeDeliverFn = deliverAutomatedIfWanted

// nudgeDeliverer is how this notifier delivers a nudge: its own deliverNudge when set, else deliverNudgeFn.
func (n *BoardNotifier) nudgeDeliverer() nudgeDeliverFn {
	if n.deliverNudge != nil {
		return n.deliverNudge
	}
	return deliverNudgeFn
}

// nudgeDue reports whether an agent with unread messages (latestID the newest) is nudged now: one nudge until the
// agent reads, which covers more messages arriving meanwhile (the agent reads them all at once), with a reminder once
// window (remindAfter, spread per agent: #3613) has passed. A new batch is nudged at once:
//   - the all-messages subscription (the orchestrator's) needs an identity watermark: after it reads batch A,
//     batch B can arrive with the same unread count before the next pass; worker subscriptions use mention-based
//     coalescing and keep one nudge per batch;
//   - in any mode, an agent that has read past the batch it was nudged for has only newer messages unread. Without
//     this a new mention that arrived before a pass saw unread == 0 waited for the reminder (#3480);
//   - an all-messages subscriber's larger unread count is new activity too.
func nudgeDue(last notifyState, notified bool, sub *board.Subscriber, unread int, latestID int64, now time.Time, window time.Duration) bool {
	newBatch := sub.ReceiveMode == "all" && last.latestID > 0 && latestID > last.latestID
	if last.latestID > 0 && sub.LastReadID >= last.latestID && latestID > last.latestID {
		newBatch = true
	}
	return !(notified && !newBatch && (unread <= last.count || sub.ReceiveMode != "all") && now.Sub(last.at) < window)
}

// nudgeKind classifies the unread messages: health is true when they are only health-monitor summaries; send is
// false for such a nudge to anyone but the orchestrator (a subscriber that may peek, or titled Orchestrator). A
// classification that fails is logged and counts as not health.
func (n *BoardNotifier) nudgeKind(ctx context.Context, sub *board.Subscriber, subscriberID, sessName string) (health, send bool) {
	health, err := n.boardStore.HasUnreadHealthMessage(ctx, sub.Project, subscriberID)
	if err != nil {
		n.logger.Warn("health message classification failed", "subscriber_id", subscriberID, "error", err)
	}
	if !health {
		return false, true
	}
	nonHealth, err := n.boardStore.HasUnreadNonHealthMessage(ctx, sub.Project, subscriberID)
	if err != nil {
		n.logger.Warn("non-health message classification failed", "subscriber_id", subscriberID, "error", err)
	}
	if nonHealth {
		return false, true
	}
	if isOrchestrator := sub.CanPeek != 0 || strings.EqualFold(strings.TrimSpace(sub.JobTitle), "Orchestrator"); !isOrchestrator {
		n.logger.Debug("suppressing health-only nudge for non-orchestrator", "subscriber_id", subscriberID, "session", sessName)
		return true, false
	}
	return true, true
}

// nudgeText is the nudge typed to the agent: the unread count, or the health check's for a health-only batch.
func nudgeText(project string, unread int, health bool) string {
	plural := "s"
	if unread == 1 {
		plural = ""
	}
	if health {
		return fmt.Sprintf("[Coral health check] Team health check for %s: %d unread message%s include a health-monitor summary. Review team status, assignments, progress, and blockers, then run 'coral-board read' to see the authoritative full content.", project, unread, plural)
	}
	return fmt.Sprintf("You have %d unread message%s on the message board. Run 'coral-board read' to see them.", unread, plural)
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
