package background

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/ptymanager"
)

// runPassWith was split for #4599 (second ranking, item 1). The notifier decides which agents are woken, so the old
// pass is kept below (verbatim but for its name, and delivering through deliverNudgeFn as the new one does) and the
// two run the same random boards, posts, reads, pauses, discoveries and failed deliveries: every nudge (recipient,
// key, text, mention, in order) and the nudge records after every pass must be the same.

// nudgeRecord is one delivery attempt as the gate would get it.
type nudgeRecord struct {
	session, key, text string
	mention            bool
	attempt            int
}

// oracleRecorder answers deliveries the same way for both notifiers: by a hash of the pass, the session, the text
// and the attempt, about one in eight failing (a pane gone) and one in twenty skipped (the agent exited).
type oracleRecorder struct {
	pass     int
	sent     []nudgeRecord
	attempts map[string]int
}

func (r *oracleRecorder) deliver(_ context.Context, _ AgentRuntime, _ *slog.Logger, name, _, _, key, text, _ string, _, mention bool, _ func(context.Context) bool) (bool, error) {
	k := name + "\x00" + text
	r.attempts[k]++
	h := fnv.New32a()
	fmt.Fprintf(h, "%d|%s|%d", r.pass, k, r.attempts[k])
	r.sent = append(r.sent, nudgeRecord{name, key, text, mention, r.attempts[k]})
	switch v := h.Sum32(); {
	case v%8 == 0:
		return false, errors.New("the pane is gone")
	case v%20 == 1:
		return true, nil
	}
	return false, nil
}

// oracleTotals counts what the runs covered, so a run that nudged nobody can't pass for a comparison.
var oracleTotals struct{ nudges, resends, health, mentions int }

func TestRunPassAgreesWithBefore4599(t *testing.T) {
	old := deliverNudgeFn
	t.Cleanup(func() { deliverNudgeFn = old })
	oracleTotals.nudges, oracleTotals.resends, oracleTotals.health, oracleTotals.mentions = 0, 0, 0, 0
	for seed := int64(1); seed <= 150; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runNotifierOracle(t, seed) })
	}
	t.Logf("compared: %d nudge attempts, %d resends after a failure, %d health nudges, %d mentions", oracleTotals.nudges, oracleTotals.resends, oracleTotals.health, oracleTotals.mentions)
	require.Greater(t, oracleTotals.nudges, 200)
	require.Greater(t, oracleTotals.resends, 5)
	require.Greater(t, oracleTotals.health, 5)
	require.Greater(t, oracleTotals.mentions, 20)
}

func runNotifierOracle(t *testing.T, seed int64) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	bs := testBoardStore(t)
	projects := []string{"p0", "p1", "p2"}[:1+rng.Intn(3)]
	titles := []string{"Orchestrator", "Developer", "QA", "Developer"}
	type member struct {
		id, title, project, sid string
	}
	var members []member
	for i := 0; i < 2+rng.Intn(6); i++ {
		m := member{id: fmt.Sprintf("A%d", i), title: titles[rng.Intn(len(titles))], project: projects[rng.Intn(len(projects))], sid: fmt.Sprintf("s%d", i)}
		var origin *string
		if rng.Intn(7) == 0 {
			r := "remote"
			origin = &r
		}
		mode := []string{"", "all", "mentions"}[rng.Intn(3)]
		_, err := bs.Subscribe(ctx, m.project, m.id, m.title, naming.SessionName("claude", m.sid), origin, mode, rng.Intn(4) == 0)
		require.NoError(t, err)
		members = append(members, m)
	}
	present := map[string]bool{}
	for _, m := range members {
		present[m.sid] = rng.Intn(5) != 0
	}
	discover := func(context.Context) ([]AgentInfo, error) {
		agents := []AgentInfo{{AgentType: "claude", SessionID: "x9", DisplayName: "Stranger"}, {AgentType: "terminal", SessionID: "t1"}, {AgentType: "claude", AgentName: "nameless"}}
		for _, m := range members {
			if present[m.sid] {
				name := m.title
				if rng.Intn(5) == 0 {
					name = "Stale Name"
				}
				agents = append(agents, AgentInfo{AgentType: "claude", SessionID: m.sid, DisplayName: name, AgentName: "a-" + m.sid})
			}
		}
		return agents, nil
	}
	paused := map[string]bool{}
	clock := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	mk := func() (*BoardNotifier, *oracleRecorder) {
		n := NewBoardNotifier(bs, &mockRuntime{}, time.Second)
		n.SetIsPausedFn(func(p string) bool { return paused[p] })
		n.now = func() time.Time { return clock }
		rec := &oracleRecorder{}
		return n, rec
	}
	oldN, oldRec := mk()
	newN, newRec := mk()
	// One discovery per pass, shared by both notifiers (the random display names stay the same within a pass).
	var passAgents []AgentInfo
	disc := func(context.Context) ([]AgentInfo, error) { return passAgents, nil }
	oldN.SetDiscoverFn(disc)
	newN.SetDiscoverFn(disc)
	if rng.Intn(2) == 0 {
		oldN.SeedFromDB(ctx)
		newN.SeedFromDB(ctx)
	}

	texts := []string{"plain update", "@all heads up", "@notify-all release", "done with it", "@Orchestrator please review", "@QA can you check", "@Developer your turn"}
	for step := 0; step < 10; step++ {
		for k := rng.Intn(4); k > 0; k-- {
			m := members[rng.Intn(len(members))]
			sender, text := m.id, texts[rng.Intn(len(texts))]
			if rng.Intn(5) == 0 {
				sender, text = "Coral Health Monitor", "[Coral team status] @Orchestrator review blockers"
			} else if rng.Intn(3) == 0 {
				text = "@" + members[rng.Intn(len(members))].id + " ping"
			}
			_, err := bs.PostMessage(ctx, m.project, sender, text, nil)
			require.NoError(t, err)
		}
		for _, m := range members {
			if rng.Intn(4) == 0 {
				_, err := bs.ReadMessages(ctx, m.project, m.id, 100)
				require.NoError(t, err)
			}
			if rng.Intn(8) == 0 {
				present[m.sid] = !present[m.sid]
			}
		}
		if rng.Intn(4) == 0 {
			p := projects[rng.Intn(len(projects))]
			paused[p] = !paused[p]
		}
		if rng.Intn(3) != 0 { // else a pass within the same seconds: a reused discovery, and its resend after a failure
			clock = clock.Add(time.Duration(rng.Intn(25)) * time.Minute)
		}
		passAgents, _ = discover(ctx)

		var boards map[string]bool
		reuse := false
		switch rng.Intn(3) {
		case 1:
			boards = map[string]bool{projects[rng.Intn(len(projects))]: true}
			reuse = true
		case 2:
			reuse = true
		}
		for _, side := range []struct {
			n   *BoardNotifier
			rec *oracleRecorder
			run func(*BoardNotifier) error
		}{
			{oldN, oldRec, func(n *BoardNotifier) error { return n.runPassWithBefore4599(ctx, boards, reuse) }},
			{newN, newRec, func(n *BoardNotifier) error { return n.runPassWith(ctx, boards, reuse) }},
		} {
			side.rec.pass, side.rec.sent, side.rec.attempts = step, nil, map[string]int{}
			deliverNudgeFn = side.rec.deliver
			require.NoError(t, side.run(side.n))
		}
		require.Equal(t, oldRec.sent, newRec.sent, "step %d (boards %v, reuse %v): the nudges", step, boards, reuse)
		for _, r := range newRec.sent {
			oracleTotals.nudges++
			if r.attempt > 1 {
				oracleTotals.resends++
			}
			if strings.Contains(r.text, "[Coral health check]") {
				oracleTotals.health++
			}
			if r.mention {
				oracleTotals.mentions++
			}
		}
		require.Equal(t, oldN.notified, newN.notified, "step %d: the nudge records", step)
		require.Equal(t, oldN.discAbsent, newN.discAbsent, "step %d: the absent subscribers", step)
	}
}

// runPassWithBefore4599 is runPassWith as it was before #4599 (second ranking, item 1), delivering through
// deliverNudgeFn.
func (n *BoardNotifier) runPassWithBefore4599(ctx context.Context, boards map[string]bool, reuse bool) error {
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
		// Counted again just before it is typed, held or not (#4362): a nudge held while the agent
		// works must not be typed after the turn in which it read those messages. A count that
		// fails keeps today's behaviour (the nudge goes).
		project, reader := sub.Project, subscriberID
		stillUnread := func(ctx context.Context) bool {
			n, err := n.boardStore.CheckUnread(ctx, project, reader)
			return err != nil || n > 0
		}
		skipped, err := deliverNudgeFn(ctx, n.runtime, n.logger, sessName, a.AgentType, a.SessionID, "board-unread:"+sub.Project, board.OneLine(nudge), "board nudge", false, mention, stillUnread)
		if err != nil && cached && !skipped {
			// The discovery was reused: if the session is still running, its pane may only
			// have moved; send once more to the same session (never another).
			if !rediscover(sessName) {
				n.logger.Info("agent gone since the last discovery; no nudge", "session", sessName)
				continue
			}
			skipped, err = deliverNudgeFn(ctx, n.runtime, n.logger, sessName, a.AgentType, a.SessionID, "board-unread:"+sub.Project, board.OneLine(nudge), "board nudge", false, mention, stillUnread)
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
