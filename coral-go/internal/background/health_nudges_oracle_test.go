package background

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/teamhealth"
)

// runProject was split for #4599 (second ranking, item 4). The health rules nudge agents and escalate to the
// orchestrator, so the old function is kept below (verbatim but for its name). Before every step board A's
// database is copied to B (the same bytes, the database's own timestamps included); the old rules run on A and the
// new on B with the same settings, agents, hook signals, held dialogs and clock; every nudge (agent, key, text, in
// order) and every row of the two databases afterwards must be equal. A floor per rule fails the test if a rule
// went unexercised: claim nudge, status nudge, claim escalation, status escalation, mention re-nudge, held-dialog
// notice, and a claim nudge a wait held back.

// healthKinds counts what the runs covered, per rule.
var (
	healthKinds   map[string]int
	healthKindsMu sync.Mutex
)

func healthAdd(kind string, n int) {
	healthKindsMu.Lock()
	healthKinds[kind] += n
	healthKindsMu.Unlock()
}

func TestRunProjectAgreesWithBefore4599(t *testing.T) {
	healthKinds = map[string]int{}
	// Each seed has its own boards, databases and random source: the seeds run in parallel (#4982: 139 s of the package on epoch),
	// inside one group so the floors are read after every seed has finished.
	t.Run("seeds", func(t *testing.T) {
		for seed := int64(1); seed <= 13; seed++ {
			t.Run(fmt.Sprint(seed), func(t *testing.T) {
				t.Parallel()
				runHealthOracle(t, seed)
			})
		}
	})
	t.Logf("compared, per rule: %v", healthKinds)
	for kind, floor := range map[string]int{
		"claim nudge": 15, "status nudge": 15, "claim escalation": 5, "status escalation": 5,
		"mention renudge": 10, "held notice": 10, "claim held back by a wait": 10, "failed delivery": 10, "budget line": 5,
		"parked task": 10, "snoozed reminder": 10,
	} {
		require.GreaterOrEqual(t, healthKinds[kind], floor, "the %s rule went (almost) unexercised", kind)
	}
}

func copyHealthBoard(t *testing.T, from, to string) {
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(to + suffix)
	}
	src, err := os.Open(from)
	require.NoError(t, err)
	defer src.Close()
	dst, err := os.Create(to)
	require.NoError(t, err)
	_, err = io.Copy(dst, src)
	require.NoError(t, err)
	require.NoError(t, dst.Close())
}

// dumpHealthBoard is every row of every table as text, sorted, without created_at: the rules stamp their board
// lines with the database's clock, which differs between the two runs by the time between them.
func dumpHealthBoard(t *testing.T, path string) []string {
	db, err := sql.Open("sqlite", path+"?_pragma=query_only(1)")
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		tables = append(tables, n)
	}
	rows.Close()
	var out []string
	for _, tbl := range tables {
		r, err := db.Query(fmt.Sprintf("SELECT * FROM %q", tbl))
		require.NoError(t, err)
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			require.NoError(t, r.Scan(ptrs...))
			var b strings.Builder
			b.WriteString(tbl)
			for i, v := range vals {
				if cols[i] == "created_at" {
					continue
				}
				if bs, ok := v.([]byte); ok {
					v = string(bs)
				}
				fmt.Fprintf(&b, " %s=%v", cols[i], v)
			}
			out = append(out, b.String())
		}
		r.Close()
	}
	sort.Strings(out)
	return out
}

func runHealthOracle(t *testing.T, seed int64) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	dir := t.TempDir()
	pathA, pathB := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	bs, err := board.NewStoreWithKey(pathA, "")
	require.NoError(t, err)
	budget := []int{3, 20, 20}[rng.Intn(3)]

	names := []string{"Orchestrator"}
	for i := 0; i < 2+rng.Intn(4); i++ {
		names = append(names, fmt.Sprintf("Dev %d", i))
	}
	sid := func(name string) string {
		return fmt.Sprintf("%08d-1111-1111-1111-111111111111", len(name)*31+int(name[len(name)-1]))
	}
	session := func(name string) string { return naming.SessionName("claude", sid(name)) }
	for _, name := range names {
		_, err := bs.Subscribe(ctx, "team", name, name, session(name), nil, "all")
		require.NoError(t, err)
	}
	// The clock: the database's own now, plus an offset that grows by minutes each step, so the posts and task
	// activity it stamps fall inside and then past the rules' windows (an hour for mentions).
	// The clock is read once, and every step's time is it plus whole minutes plus 30 s: the database stamps to the
	// second, moments after it, so no comparison with a whole-minute rule window lands within seconds of its
	// boundary, and the same seed gives the same result on every run.
	offset := 30 * time.Second
	base := time.Now().UTC()
	signals := map[string]agentSignal{}
	known := map[string]bool{}
	held := map[string]time.Time{}
	var taskIDs []int64
	for step := 0; step < 24; step++ {
		offset += time.Duration(2+rng.Intn(9)) * time.Minute
		now := base.Add(offset)
		hs := healthOn()
		hs.MentionRenudge.On = rng.Intn(5) != 0
		hs.HeldNotice.On = rng.Intn(5) != 0
		hs.ClaimNudge.AfterMin = []int{5, 10}[rng.Intn(2)]
		hs.ClaimNudge.Nudges = []int{1, 1, 1, 2}[rng.Intn(4)] // mostly one: escalations come sooner
		hs.StatusNudge.AfterMin = []int{5, 20}[rng.Intn(2)]
		hs.StatusNudge.EscalateAfterMin = []int{5, 10}[rng.Intn(2)]
		hs.MentionRenudge.AfterMin = []int{5, 10}[rng.Intn(2)]
		hs.HeldNotice.AfterMin = []int{5, 10}[rng.Intn(2)]

		bySession := map[string]AgentInfo{}
		for _, name := range names {
			if rng.Intn(6) != 0 {
				bySession[session(name)] = AgentInfo{AgentType: "claude", SessionID: sid(name), DisplayName: name}
			}
			known[sid(name)] = rng.Intn(5) != 0
			signals[sid(name)] = agentSignal{
				InTurn: rng.Intn(4) == 0, NeedsInput: rng.Intn(8) == 0,
				Since:     now.Add(-time.Duration(rng.Intn(40)) * time.Minute),
				LastEvent: now.Add(-time.Duration([]int{0, 1, rng.Intn(60), rng.Intn(90)}[rng.Intn(4)]) * time.Minute),
			}
			// A dialog holding this agent's automated messages: since a few minutes ago, or released.
			if _, ok := held[session(name)]; ok && rng.Intn(3) == 0 {
				delete(held, session(name))
			} else if !ok && rng.Intn(5) == 0 {
				held[session(name)] = now.Add(-time.Duration(rng.Intn(30)) * time.Minute)
			}
		}
		for k := rng.Intn(4); k > 0; k-- {
			assignee := names[1+rng.Intn(len(names)-1)]
			if rng.Intn(5) == 0 {
				assignee = ""
			} else if rng.Intn(4) == 0 {
				assignee = "  " + strings.ToUpper(assignee) // the same member, spelled another way (board.SameIdentity)
			}
			task, err := bs.CreateTask(ctx, "team", "work", "", []string{"high", "medium", "low"}[rng.Intn(3)], "Operator", assignee)
			require.NoError(t, err)
			taskIDs = append(taskIDs, task.ID)
		}
		for k := rng.Intn(3); k > 0 && len(taskIDs) > 0; k-- {
			id := taskIDs[rng.Intn(len(taskIDs))]
			task, err := bs.GetTask(ctx, "team", id)
			if err != nil || task == nil {
				continue
			}
			switch op := rng.Intn(8); {
			case op == 6: // parked: set aside, for good or until a time later steps pass
				if rng.Intn(4) == 0 {
					bs.UnparkTask(ctx, "team", id)
				} else {
					bs.ParkTask(ctx, "team", id, "waiting on the operator", []time.Time{{}, now.Add(10 * time.Minute), now.Add(40 * time.Minute)}[rng.Intn(3)])
				}
			case op == 7: // a task in progress has its status reminder snoozed until a time later steps pass
				open, _ := bs.ListOpenTasks(ctx, "team")
				for _, ot := range open {
					if ot.Status == "in_progress" && rng.Intn(2) == 0 {
						bs.SnoozeTaskReminder(ctx, "team", ot.ID, now.Add(time.Duration([]int{5, 30}[rng.Intn(2)])*time.Minute))
						break
					}
				}
			case op <= 2 && task.AssignedTo != nil:
				bs.ClaimTask(ctx, "team", strings.TrimSpace(*task.AssignedTo), id)
			case op == 3 && task.AssignedTo != nil:
				bs.CompleteTask(ctx, "team", id, strings.TrimSpace(*task.AssignedTo), nil)
			case op == 4:
				bs.CancelTask(ctx, "team", id, "Operator", nil)
			default:
				bs.ReassignTask(ctx, "team", id, names[1+rng.Intn(len(names)-1)])
			}
		}
		// A member waits on someone (some waits short enough to expire).
		if rng.Intn(3) == 0 {
			who := names[1+rng.Intn(len(names)-1)]
			bs.RegisterWait(ctx, "team", who, session(who), "message", "Orchestrator", "", []time.Duration{2 * time.Minute, time.Hour}[rng.Intn(2)])
		}
		// A teammate mentions the orchestrator; the orchestrator may read or answer it before it is due.
		if rng.Intn(3) != 0 {
			from := names[1+rng.Intn(len(names)-1)]
			_, err := bs.PostMessage(ctx, "team", from, "@Orchestrator a question about my task", nil)
			require.NoError(t, err)
		}
		switch rng.Intn(6) {
		case 0:
			bs.ReadMessages(ctx, "team", "Orchestrator", 100)
		case 1: // the orchestrator acted since (a hook event now): no re-nudge for what came before. A post would do
			// the same, but the database stamps it to the second, and a post and a mention made within one second
			// compare as equal or not by chance, which made the same seed differ from run to run.
			known[sid("Orchestrator")] = true
			sig := signals[sid("Orchestrator")]
			sig.LastEvent = now
			signals[sid("Orchestrator")] = sig
		}
		// Claims a wait holds back: a running, known, idle member with assigned work and nothing in progress.
		waits, _ := bs.ListActiveWaits(ctx, "team")
		tasks, _ := bs.ListOpenTasks(ctx, "team")
		for _, task := range tasks { // assigned work set aside: parked, or its status reminder snoozed
			if task.AssignedTo == nil {
				continue
			}
			if task.Parked(now) {
				healthAdd("parked task", 1)
			}
			if until, _ := bs.TaskSnoozedUntil(ctx, "team", task.ID); task.Status == "in_progress" && until.After(now) {
				healthAdd("snoozed reminder", 1)
			}
		}
		for _, w := range waits {
			sig := signals[sid(w.SubscriberID)]
			mine, pending := agentTasks(tasks, w.SubscriberID, now)
			if _, live := bySession[session(w.SubscriberID)]; live && known[sid(w.SubscriberID)] && !sig.InTurn && !sig.NeedsInput &&
				len(mine) == 0 && len(pending) > 0 && now.Sub(sig.Since) >= time.Duration(hs.ClaimNudge.AfterMin)*time.Minute {
				healthAdd("claim held back by a wait", 1)
			}
		}

		// The same bytes on both sides.
		require.NoError(t, bs.Close())
		copyHealthBoard(t, pathA, pathB)
		var sent [2][]string
		var failed [2]int
		for i, p := range []string{pathA, pathB} {
			store, err := board.NewStoreWithKey(p, "")
			require.NoError(t, err)
			tries := map[string]int{}
			h := &HealthNudges{
				store:       store,
				signal:      func(_ context.Context, id string) (agentSignal, bool) { return signals[id], known[id] },
				now:         func() time.Time { return now },
				heldDialogs: func() map[string]time.Time { return held },
				TeamBudget:  budget,
				deliver: func(_ context.Context, a AgentInfo, key, text string) error {
					k := a.DisplayName + "|" + key + "|" + text
					sent[i] = append(sent[i], k)
					// Which deliveries fail: by the agent and the gate key, not the text, which can carry a
					// clock time (a mention re-nudge's "since 15:04") and would make the same seed differ by
					// the time of day.
					who := a.DisplayName + "|" + key
					tries[who]++
					hh := fnv.New32a()
					fmt.Fprintf(hh, "%d|%s|%d", step, who, tries[who])
					if hh.Sum32()%6 == 0 {
						failed[i]++
						return errors.New("not delivered")
					}
					return nil
				},
			}
			if i == 0 {
				h.runProjectBefore4599(ctx, "team", bySession, hs)
			} else {
				h.runProject(ctx, "team", bySession, hs)
			}
			require.NoError(t, store.Close())
		}
		require.Equal(t, sent[0], sent[1], "step %d: the nudges", step)
		require.Equal(t, failed[0], failed[1], "step %d: the failed deliveries", step)
		before := dumpHealthBoard(t, pathA)
		require.Equal(t, before, dumpHealthBoard(t, pathB), "step %d: the databases after the run", step)
		for _, k := range sent[1] {
			for kind, key := range map[string]string{"claim nudge": "|health-claim:", "status nudge": "|health-status:", "mention renudge": "|health-mention:"} {
				if strings.Contains(k, key) {
					healthAdd(kind, 1)
				}
			}
		}
		healthAdd("failed delivery", failed[1])

		bs, err = board.NewStoreWithKey(pathA, "")
		require.NoError(t, err)
	}
	msgs, err := bs.ListMessages(ctx, "team", 1000, 0, 0)
	require.NoError(t, err)
	for _, m := range msgs {
		c := m.Content
		switch {
		case strings.Contains(c, "escalated to the orchestrator:"):
			healthKinds["claim escalation"] += strings.Count(c, "has not claimed")
			healthKinds["status escalation"] += strings.Count(c, "silent on #")
		case strings.Contains(c, "waiting at a dialog"):
			healthAdd("held notice", 1)
		case strings.Contains(c, "[Coral health actions paused]"):
			healthAdd("budget line", 1)
		}
	}
	require.NoError(t, bs.Close())
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
