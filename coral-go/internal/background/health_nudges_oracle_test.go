package background

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/teamhealth"
)

// runProject was split for #4599 (second ranking, item 4). The health rules nudge agents and escalate to the
// orchestrator, so the old function is kept below (verbatim but for its name) and the two run the same random teams
// on two boards built step by step the same way: every nudge (agent, key, text, in order), the board's health lines,
// every claim and status counter and the open escalations must stay the same.

var healthOracleTotals struct{ nudges, status, failed, lines, counters int }

func TestRunProjectAgreesWithBefore4599(t *testing.T) {
	healthOracleTotals.nudges, healthOracleTotals.status, healthOracleTotals.failed, healthOracleTotals.lines, healthOracleTotals.counters = 0, 0, 0, 0, 0
	for seed := int64(1); seed <= 80; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runHealthOracle(t, seed) })
	}
	t.Logf("compared: %d nudges (%d status, %d failed deliveries), %d board lines, %d counter entries", healthOracleTotals.nudges, healthOracleTotals.status, healthOracleTotals.failed, healthOracleTotals.lines, healthOracleTotals.counters)
	require.Greater(t, healthOracleTotals.status, 30)
	require.Greater(t, healthOracleTotals.nudges, 100)
	require.Greater(t, healthOracleTotals.failed, 5)
	require.Greater(t, healthOracleTotals.lines, 50)
	require.Greater(t, healthOracleTotals.counters, 10)
}

// healthSide is one of the two: its board, its rules and what it delivered.
type healthSide struct {
	bs     *board.Store
	h      *HealthNudges
	sent   []string
	failed int
	step   int
	tries  map[string]int
}

func runHealthOracle(t *testing.T, seed int64) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	// A day ahead of the database's own clock, which stamps the tasks' activity: their activity is then in the past
	// and the status rule acts.
	now := time.Now().UTC().Truncate(time.Second).Add(24 * time.Hour)
	agents := []AgentInfo{}
	signals := map[string]agentSignal{}
	known := map[string]bool{}
	mk := func() *healthSide {
		bs, err := board.NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
		require.NoError(t, err)
		t.Cleanup(func() { bs.Close() })
		s := &healthSide{bs: bs, tries: map[string]int{}}
		s.h = &HealthNudges{
			store:    bs,
			discover: func(context.Context) ([]AgentInfo, error) { return agents, nil },
			signal: func(_ context.Context, id string) (agentSignal, bool) {
				return signals[id], known[id]
			},
			deliver: func(_ context.Context, a AgentInfo, key, text string) error {
				k := a.DisplayName + "|" + key + "|" + text
				s.tries[k]++
				s.sent = append(s.sent, k)
				h := fnv.New32a()
				fmt.Fprintf(h, "%d|%s|%d", s.step, k, s.tries[k])
				if h.Sum32()%6 == 0 {
					s.failed++
					return errors.New("not delivered")
				}
				return nil
			},
			now:         func() time.Time { return now },
			heldDialogs: func() map[string]time.Time { return nil },
		}
		return s
	}
	budget := []int{3, 20}[rng.Intn(2)]
	oldS, newS := mk(), mk()
	oldS.h.TeamBudget, newS.h.TeamBudget = budget, budget
	sides := []*healthSide{oldS, newS}
	both := func(f func(bs *board.Store) error) {
		for _, s := range sides {
			require.NoError(t, f(s.bs))
		}
	}

	names := []string{"Orchestrator"}
	for i := 0; i < 2+rng.Intn(4); i++ {
		names = append(names, fmt.Sprintf("Dev %d", i))
	}
	sids := map[string]string{}
	for i, name := range names {
		sid := fmt.Sprintf("%08d-1111-1111-1111-111111111111", i)
		sids[name] = sid
		title := name
		both(func(bs *board.Store) error {
			_, err := bs.Subscribe(ctx, "team", name, title, naming.SessionName("claude", sid), nil, "all")
			return err
		})
	}
	var taskIDs []int64
	for step := 0; step < 16; step++ {
		// Who runs, and what their hooks say.
		agents = agents[:0]
		for _, name := range names {
			sid := sids[name]
			if rng.Intn(6) != 0 {
				agents = append(agents, AgentInfo{AgentType: "claude", SessionID: sid, DisplayName: name})
			}
			known[sid] = rng.Intn(5) != 0
			signals[sid] = agentSignal{
				InTurn: rng.Intn(4) == 0, NeedsInput: rng.Intn(8) == 0,
				Since:     now.Add(-time.Duration(rng.Intn(40)) * time.Minute),
				LastEvent: now.Add(-time.Duration([]int{0, 1, rng.Intn(60), rng.Intn(60)}[rng.Intn(4)]) * time.Minute), // often just now: activity
			}
		}
		// New work, and some of it claimed.
		for k := rng.Intn(3); k > 0; k-- {
			assignee := names[1+rng.Intn(len(names)-1)]
			if rng.Intn(5) == 0 {
				assignee = ""
			}
			if assignee != "" && rng.Intn(4) == 0 {
				assignee = "  " + strings.ToUpper(assignee) // the same member, spelled another way (board.SameIdentity)
			}
			prio := []string{"high", "medium", "low"}[rng.Intn(3)]
			var id int64
			for _, s := range sides {
				task, err := s.bs.CreateTask(ctx, "team", "work", "", prio, "Operator", assignee)
				require.NoError(t, err)
				id = task.ID
			}
			taskIDs = append(taskIDs, id)
		}
		// Work moves: claimed (mostly by its assignee), finished, cancelled or reassigned; each may be refused, the
		// same way on both boards.
		for k := rng.Intn(3); k > 0 && len(taskIDs) > 0; k-- {
			id := taskIDs[rng.Intn(len(taskIDs))]
			var assignee string
			if task, err := oldS.bs.GetTask(ctx, "team", id); err == nil && task != nil && task.AssignedTo != nil {
				assignee = *task.AssignedTo
			}
			who := names[1+rng.Intn(len(names)-1)]
			op := rng.Intn(6)
			for _, s := range sides {
				switch {
				case op <= 2 && assignee != "":
					s.bs.ClaimTask(ctx, "team", assignee, id)
				case op == 3:
					s.bs.CompleteTask(ctx, "team", id, assignee, nil)
				case op == 4:
					s.bs.CancelTask(ctx, "team", id, "Operator", nil)
				default:
					s.bs.ReassignTask(ctx, "team", id, who)
				}
			}
		}
		// A teammate mentions the orchestrator.
		if rng.Intn(3) == 0 {
			from := names[1+rng.Intn(len(names)-1)]
			both(func(bs *board.Store) error {
				_, err := bs.PostMessage(ctx, "team", from, "@Orchestrator a question", nil)
				return err
			})
		}
		now = now.Add(time.Duration(1+rng.Intn(15)) * time.Minute)

		for i, s := range sides {
			s.step, s.sent, s.tries, s.failed = step, nil, map[string]int{}, 0
			bySession := map[string]AgentInfo{}
			for _, a := range agents {
				bySession[naming.SessionName(a.AgentType, a.SessionID)] = a
			}
			hs := s.bs.GetHealthSettings(ctx, "team")
			if i == 0 {
				s.h.runProjectBefore4599(ctx, "team", bySession, hs)
			} else {
				s.h.runProject(ctx, "team", bySession, hs)
			}
		}
		require.Equal(t, oldS.sent, newS.sent, "step %d: the nudges", step)
		require.Equal(t, healthLines(t, oldS.bs), healthLines(t, newS.bs), "step %d: the board lines", step)
		require.Equal(t, healthCounters(t, oldS.bs, names, taskIDs), healthCounters(t, newS.bs, names, taskIDs), "step %d: the counters", step)
		healthOracleTotals.nudges += len(newS.sent)
		for _, k := range newS.sent {
			if strings.Contains(k, "health-status:") {
				healthOracleTotals.status++
			}
		}
		require.Equal(t, oldS.failed, newS.failed, "step %d: the failed deliveries", step)
		healthOracleTotals.failed += newS.failed
		healthOracleTotals.counters += len(healthCounters(t, newS.bs, names, taskIDs))
	}
	healthOracleTotals.lines += len(healthLines(t, newS.bs))
}

func healthLines(t *testing.T, bs *board.Store) []string {
	msgs, err := bs.ListMessages(context.Background(), "team", 1000, 0, 0)
	require.NoError(t, err)
	var out []string
	for _, m := range msgs {
		if strings.Contains(m.Content, "[Coral health") {
			out = append(out, m.Content)
		}
	}
	return out
}

// healthCounters: every non-zero claim and status counter, and the open escalations.
func healthCounters(t *testing.T, bs *board.Store, names []string, taskIDs []int64) []string {
	ctx := context.Background()
	var out []string
	for _, name := range names {
		for _, id := range taskIDs {
			for _, rule := range []string{ruleClaimNudge, ruleStatusNudge} {
				c, err := bs.GetHealthCounter(ctx, "team", rule, name, fmt.Sprint(id))
				require.NoError(t, err)
				if c != (board.HealthCounter{}) {
					out = append(out, fmt.Sprintf("%s %s #%d %+v", rule, name, id, c))
				}
			}
		}
	}
	esc, err := bs.HealthEscalations(ctx, "team")
	require.NoError(t, err)
	for _, e := range esc {
		out = append(out, fmt.Sprintf("escalation %+v", e))
	}
	sort.Strings(out)
	return out
}

// runProjectBefore4599 is runProject as it was before #4599 (second ranking, item 4).
func (h *HealthNudges) runProjectBefore4599(ctx context.Context, project string, bySession map[string]AgentInfo, hs board.HealthSettings) {
	minutes := func(m int) time.Duration { return time.Duration(m) * time.Minute }
	claimAfter, claimNudges := minutes(hs.ClaimNudge.AfterMin), hs.ClaimNudge.Nudges
	statusAfter, silentAfter := minutes(hs.StatusNudge.AfterMin), minutes(hs.StatusNudge.EscalateAfterMin)
	subs, err := h.store.ListSubscribers(ctx, project)
	if err != nil {
		return
	}
	tasks, err := h.store.ListOpenTasks(ctx, project)
	if err != nil {
		return
	}
	waits, _ := h.store.ListActiveWaits(ctx, project)
	waiting := map[string]bool{}
	for _, w := range waits {
		waiting[w.SubscriberID] = true
	}
	now := h.now()
	limits := func(cooldown time.Duration) board.HealthLimits {
		return board.HealthLimits{Cooldown: cooldown, PerTargetPerHour: 3, TeamPerHour: h.TeamBudget}
	}
	var claimed, statused, escalated, renudged []string
	budgetHit := false
	take := func(rule, target string, cooldown time.Duration) bool {
		why, err := h.store.TakeHealthAction(ctx, project, rule, target, now, limits(cooldown))
		if why == board.HealthRefusedBudget {
			budgetHit = true
		}
		return err == nil && why == ""
	}
	for _, sub := range subs {
		a, live := bySession[sub.SessionName]
		if !live {
			continue
		}
		var mine, pending []board.Task
		for _, t := range tasks {
			if t.AssignedTo == nil || !board.SameIdentity(*t.AssignedTo, sub.SubscriberID) || t.Parked(now) {
				continue // a parked task is set aside on purpose: no nudge for it (#4175 step 3)
			}
			switch t.Status {
			case "in_progress":
				mine = append(mine, t)
			case "pending":
				pending = append(pending, t)
			}
		}
		sig, known := h.signal(ctx, a.SessionID)

		// Claim nudge: idle, nothing in progress, assigned work waiting.
		if len(mine) > 0 {
			h.store.ResetHealthCounters(ctx, project, ruleClaimNudge, sub.SubscriberID)
		} else if hs.ClaimNudge.On && len(pending) > 0 && known && !sig.InTurn && !sig.NeedsInput && !waiting[sub.SubscriberID] &&
			!sig.Since.IsZero() && now.Sub(sig.Since) >= claimAfter {
			t := pending[0] // ListOpenTasks orders by priority, then id
			subject := fmt.Sprint(t.ID)
			c, err := h.store.GetHealthCounter(ctx, project, ruleClaimNudge, sub.SubscriberID, subject)
			switch {
			case err != nil || c.Escalated:
			case c.Count >= claimNudges:
				if h.store.MarkHealthEscalated(ctx, project, ruleClaimNudge, sub.SubscriberID, subject, now) == nil {
					escalated = append(escalated, fmt.Sprintf("%s has not claimed #%d after %d nudges", teamhealth.Name(sub.SubscriberID), t.ID, c.Count))
				}
			case take(ruleClaimNudge, sub.SubscriberID, claimAfter):
				if h.deliver(ctx, a, "health-claim:"+project, claimNudgeText(t.ID)) == nil {
					h.store.BumpHealthCounter(ctx, project, ruleClaimNudge, sub.SubscriberID, subject, now)
					claimed = append(claimed, fmt.Sprintf("%s (#%d)", teamhealth.Name(sub.SubscriberID), t.ID))
				}
			}
		}

		// Status nudge: a task in progress with no activity.
		for _, t := range mine {
			if !hs.StatusNudge.On {
				break
			}
			if until, _ := h.store.TaskSnoozedUntil(ctx, project, t.ID); until.After(now) {
				continue // the reminder is snoozed (long tests, a release gate)
			}
			subject := fmt.Sprint(t.ID)
			last, _ := h.store.TaskLastActivity(ctx, project, t.ID)
			if known && sig.LastEvent.After(last) {
				last = sig.LastEvent
			}
			if last.IsZero() {
				continue
			}
			c, err := h.store.GetHealthCounter(ctx, project, ruleStatusNudge, sub.SubscriberID, subject)
			if err != nil {
				continue
			}
			if c.Count > 0 || c.Escalated {
				if at, perr := time.Parse("2006-01-02T15:04:05.000Z", c.LastAt); perr == nil && last.After(at) {
					h.store.ResetHealthCounter(ctx, project, ruleStatusNudge, sub.SubscriberID, subject) // active again
					c = board.HealthCounter{}
				} else if !c.Escalated && perr == nil && now.Sub(at) >= silentAfter {
					if h.store.MarkHealthEscalated(ctx, project, ruleStatusNudge, sub.SubscriberID, subject, now) == nil {
						escalated = append(escalated, fmt.Sprintf("%s silent on #%d after a status nudge", teamhealth.Name(sub.SubscriberID), t.ID))
					}
					continue
				} else {
					continue
				}
			}
			if silent := now.Sub(last); silent >= statusAfter && take(ruleStatusNudge, sub.SubscriberID, statusAfter) {
				if h.deliver(ctx, a, "health-status:"+project, statusNudgeText(t.ID, int(silent.Minutes()))) == nil {
					h.store.BumpHealthCounter(ctx, project, ruleStatusNudge, sub.SubscriberID, subject, now)
					statused = append(statused, fmt.Sprintf("%s (#%d)", teamhealth.Name(sub.SubscriberID), t.ID))
				}
			}
		}
	}

	// Re-nudge the orchestrator, once, for agents' @-mentions it has not acted on (#4176 rule 9):
	// once per message, one nudge per sender per window, never for Coral's own lines.
	if orch, err := h.store.TeamOrchestrator(ctx, project); err == nil && hs.MentionRenudge.On {
		h.renudgeMentions(ctx, project, orch, subs, bySession, now, minutes(hs.MentionRenudge.AfterMin), take, &renudged)
	}

	// An agent whose messages wait at a dialog: tell the Operator (#4175 step 3).
	h.heldNotices(ctx, project, subs, now, hs, take)

	// An escalation ends with its task (completed, cancelled, reassigned).
	if esc, err := h.store.HealthEscalations(ctx, project); err == nil {
		open := map[string]bool{}
		for _, t := range tasks {
			if t.AssignedTo != nil && (t.Status == "pending" || t.Status == "in_progress") {
				open[*t.AssignedTo+"|"+fmt.Sprint(t.ID)] = true
			}
		}
		for _, e := range esc {
			if !open[e.Target+"|"+e.Subject] {
				h.store.ResetHealthCounter(ctx, project, e.Rule, e.Target, e.Subject)
			}
		}
	}

	// One board line per rule (#4176 rule 6), from Coral's task-queue sender: not activity,
	// not unread, not a mention.
	line := func(what string, items []string) {
		if len(items) == 0 {
			return
		}
		sort.Strings(items)
		h.store.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf("[Coral health] %s: %s", what, strings.Join(items, "; ")), nil)
	}
	line("claim nudge sent to", claimed)
	line("status asked of", statused)
	line("escalated to the orchestrator", escalated)
	line("orchestrator reminded of mentions from", renudged)
	if budgetHit {
		if why, err := h.store.TakeHealthAction(ctx, project, "budget-reached", "board", now, board.HealthLimits{Cooldown: time.Hour}); err == nil && why == "" {
			h.store.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf(
				"[Coral health actions paused] this board's budget of %d automatic actions an hour is used up; Coral acts again next hour", h.TeamBudget), nil)
		}
	}
}
