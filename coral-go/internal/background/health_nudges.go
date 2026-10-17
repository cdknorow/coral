package background

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/sessionstate"
	"github.com/cdknorow/coral/internal/store"
)

// HealthNudges is the part of the health check that acts by itself (#4175 step 1): it nudges an
// idle agent to claim the work assigned to it, and asks an agent whose task shows no activity
// for a one-line status; after the set number of nudges it stops and escalates to the
// orchestrator's check. Security's #4176:
//   - rule 1: every nudge and board line is a fixed template with task numbers and members'
//     names only;
//   - rule 5: every nudge goes through board.TakeHealthAction (cooldown, per-agent and team
//     limits), and the escalate-after counters are stored;
//   - rule 6: one board line per rule per run (the run is at least five minutes apart);
//   - rule 10: nudges are typed through the gate, held while the agent is in a turn or shows
//     a prompt (never answering one);
//   - rule 11: Coral never reads the reply; activity is metadata only (hook events, task times).
type HealthNudges struct {
	store    *board.Store
	runtime  AgentRuntime
	logger   *slog.Logger
	discover func(ctx context.Context) ([]AgentInfo, error)
	signal   func(ctx context.Context, sessionID string) (agentSignal, bool)
	deliver  func(ctx context.Context, a AgentInfo, key, text string) error
	now      func() time.Time

	Interval    time.Duration
	ClaimAfter  time.Duration // idle with assigned work this long: a claim nudge
	ClaimNudges int           // nudges before escalating
	StatusAfter time.Duration // an in-progress task with no activity this long: a status nudge
	SilentAfter time.Duration // still nothing this long after the status nudge: escalate
	TeamBudget  int           // automatic actions per board per hour
}

// agentSignal is an agent's state from its hook events.
type agentSignal struct {
	InTurn, NeedsInput bool
	Since              time.Time // when its current state began
	LastEvent          time.Time // its latest hook event
}

// NewHealthNudges returns the rules with the design's defaults.
func NewHealthNudges(bs *board.Store, ts *store.TaskStore, rt AgentRuntime, discover func(context.Context) ([]AgentInfo, error)) *HealthNudges {
	h := &HealthNudges{
		store: bs, runtime: rt, logger: slog.Default(), discover: discover, now: time.Now,
		Interval: 5 * time.Minute, ClaimAfter: 10 * time.Minute, ClaimNudges: 2,
		StatusAfter: 20 * time.Minute, SilentAfter: 10 * time.Minute, TeamBudget: 20,
	}
	h.signal = func(ctx context.Context, sessionID string) (agentSignal, bool) {
		events, err := ts.GetSessionStateEvents(ctx, []string{sessionID})
		if err != nil || len(events[sessionID]) == 0 {
			return agentSignal{}, false // no hook events: unknown, never nudged
		}
		var in sessionstate.Input
		for _, ev := range events[sessionID] {
			in.Events = append(in.Events, ev.StateEvent())
		}
		st := sessionstate.Derive(in)
		return agentSignal{InTurn: st.InTurn, NeedsInput: st.NeedsInput, Since: st.Since, LastEvent: st.LastEvent}, true
	}
	h.deliver = func(ctx context.Context, a AgentInfo, key, text string) error {
		_, err := deliverAutomated(ctx, h.runtime, h.logger, naming.SessionName(a.AgentType, a.SessionID), a.AgentType, a.SessionID, key, board.OneLine(text), "health nudge", false, false)
		return err
	}
	return h
}

// Run applies the rules every Interval until ctx is done.
func (h *HealthNudges) Run(ctx context.Context) error {
	return runEvery(ctx, h.Interval, h.Interval, func() { h.RunOnce(ctx) })
}

// Rule names (health_actions.rule and the counters).
const (
	ruleClaimNudge  = "claim-nudge"
	ruleStatusNudge = "status-nudge"
)

func claimNudgeText(id int64) string {
	return fmt.Sprintf("[Coral] #%d is assigned to you and waiting. Run 'coral-board task claim %d' to start it.", id, id)
}

func statusNudgeText(id int64, minutes int) string {
	return fmt.Sprintf("[Coral] No activity on #%d for %d minutes. Post one line on where you are: coral-board post \"#%d: ...\"", id, minutes, id)
}

// RunOnce applies the rules to every board once.
func (h *HealthNudges) RunOnce(ctx context.Context) {
	projects, err := h.store.ListProjects(ctx)
	if err != nil {
		return
	}
	agents, err := h.discover(ctx)
	if err != nil {
		return
	}
	bySession := map[string]AgentInfo{}
	for _, a := range agents {
		bySession[naming.SessionName(a.AgentType, a.SessionID)] = a
	}
	for _, p := range projects {
		if v, _ := h.store.BoardSetting(ctx, p.Project, "health_nudges"); v == "off" {
			continue
		}
		h.runProject(ctx, p.Project, bySession)
	}
}

func (h *HealthNudges) runProject(ctx context.Context, project string, bySession map[string]AgentInfo) {
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
	var claimed, statused, escalated []string
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
			if t.AssignedTo == nil || !board.SameIdentity(*t.AssignedTo, sub.SubscriberID) {
				continue
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
		} else if len(pending) > 0 && known && !sig.InTurn && !sig.NeedsInput && !waiting[sub.SubscriberID] &&
			!sig.Since.IsZero() && now.Sub(sig.Since) >= h.ClaimAfter {
			t := pending[0] // ListOpenTasks orders by priority, then id
			subject := fmt.Sprint(t.ID)
			c, err := h.store.GetHealthCounter(ctx, project, ruleClaimNudge, sub.SubscriberID, subject)
			switch {
			case err != nil || c.Escalated:
			case c.Count >= h.ClaimNudges:
				if h.store.MarkHealthEscalated(ctx, project, ruleClaimNudge, sub.SubscriberID, subject, now) == nil {
					escalated = append(escalated, fmt.Sprintf("%s has not claimed #%d after %d nudges", sub.SubscriberID, t.ID, c.Count))
				}
			case take(ruleClaimNudge, sub.SubscriberID, h.ClaimAfter):
				if h.deliver(ctx, a, "health-claim:"+project, claimNudgeText(t.ID)) == nil {
					h.store.BumpHealthCounter(ctx, project, ruleClaimNudge, sub.SubscriberID, subject, now)
					claimed = append(claimed, fmt.Sprintf("%s (#%d)", sub.SubscriberID, t.ID))
				}
			}
		}

		// Status nudge: a task in progress with no activity.
		for _, t := range mine {
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
				} else if !c.Escalated && perr == nil && now.Sub(at) >= h.SilentAfter {
					if h.store.MarkHealthEscalated(ctx, project, ruleStatusNudge, sub.SubscriberID, subject, now) == nil {
						escalated = append(escalated, fmt.Sprintf("%s silent on #%d after a status nudge", sub.SubscriberID, t.ID))
					}
					continue
				} else {
					continue
				}
			}
			if silent := now.Sub(last); silent >= h.StatusAfter && take(ruleStatusNudge, sub.SubscriberID, h.StatusAfter) {
				if h.deliver(ctx, a, "health-status:"+project, statusNudgeText(t.ID, int(silent.Minutes()))) == nil {
					h.store.BumpHealthCounter(ctx, project, ruleStatusNudge, sub.SubscriberID, subject, now)
					statused = append(statused, fmt.Sprintf("%s (#%d)", sub.SubscriberID, t.ID))
				}
			}
		}
	}

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
	if budgetHit {
		if why, err := h.store.TakeHealthAction(ctx, project, "budget-reached", "board", now, board.HealthLimits{Cooldown: time.Hour}); err == nil && why == "" {
			h.store.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf(
				"[Coral health actions paused] this board's budget of %d automatic actions an hour is used up; Coral acts again next hour", h.TeamBudget), nil)
		}
	}
}
