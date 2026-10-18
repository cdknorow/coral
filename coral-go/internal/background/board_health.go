package background

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/teamhealth"
)

// BoardHealthMonitor is the orchestrator's check (#4175 step 2): every interval, for each board,
// what needs the orchestrator's judgment, ranked, with evidence and a suggestion (escalations from
// Coral's own rules, an agent with no work, a reviewer's growing queue, queue problems), then one
// line on what moved; a single all-clear line when nothing needs it. It is typed to the
// orchestrator through the gate (held while it is in a turn or at a prompt), never posted to the
// board, and at most teamhealth.MaxCheck bytes (Security's #4176 rule 3). Agent text appears
// only through teamhealth.ShownText (rule 2).
type BoardHealthMonitor struct {
	store     *board.Store
	interval  time.Duration
	idleAfter time.Duration // idle with nothing assigned this long: an item
	logger    *slog.Logger
	now       func() time.Time

	discover func(ctx context.Context) ([]AgentInfo, error)
	signal   func(ctx context.Context, sessionID string) (agentSignal, bool)
	deliver  func(ctx context.Context, a AgentInfo, key, text string) error

	mu        sync.Mutex
	lastCheck map[string]time.Time // per board
}

func NewBoardHealthMonitor(store *board.Store, interval time.Duration) *BoardHealthMonitor {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &BoardHealthMonitor{store: store, interval: interval, idleAfter: 30 * time.Minute, logger: slog.Default(), now: time.Now, lastCheck: map[string]time.Time{}}
}

// SetAgents gives the check its view of the agents (hook events, discovery) and the gate to type
// through. Without it the check has nobody to tell and does nothing.
func (m *BoardHealthMonitor) SetAgents(ts *store.TaskStore, rt AgentRuntime, discover func(context.Context) ([]AgentInfo, error)) {
	m.discover = discover
	m.signal = hookSignal(ts)
	m.deliver = func(ctx context.Context, a AgentInfo, key, text string) error {
		_, err := deliverAutomated(ctx, rt, m.logger, naming.SessionName(a.AgentType, a.SessionID), a.AgentType, a.SessionID, key, board.OneLine(text), "health check", false, false)
		return err
	}
}

// SetIdleThresholds sets how long an agent with nothing assigned is idle before the check names it
// (the first value; the second is no longer used: Coral's status nudge escalates stale tasks).
func (m *BoardHealthMonitor) SetIdleThresholds(remindAfter, _ time.Duration) {
	if remindAfter > 0 {
		m.idleAfter = remindAfter
	}
}

func (m *BoardHealthMonitor) Run(ctx context.Context) error {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			m.scan(ctx)
		}
	}
}

func (m *BoardHealthMonitor) scan(ctx context.Context) {
	if m.deliver == nil || m.discover == nil {
		return
	}
	projects, err := m.store.ListProjects(ctx)
	if err != nil {
		return
	}
	agents, err := m.discover(ctx)
	if err != nil {
		return
	}
	bySession := map[string]AgentInfo{}
	for _, a := range agents {
		bySession[naming.SessionName(a.AgentType, a.SessionID)] = a
	}
	for _, p := range projects {
		m.checkProject(ctx, p.Project, bySession)
	}
}

// checkProject composes one board's check and types it to its recipient, as the team's health
// settings say (on, interval, recipient, all-clear line).
func (m *BoardHealthMonitor) checkProject(ctx context.Context, project string, bySession map[string]AgentInfo) {
	hs := m.store.GetHealthSettings(ctx, project)
	if !hs.On {
		return
	}
	// A long wait is escalated to who it waits on at every scan, not only when the check goes out (#4583).
	m.escalateWaits(ctx, project)
	m.mu.Lock()
	last, checked := m.lastCheck[project]
	m.mu.Unlock()
	if checked && m.now().Sub(last) < time.Duration(hs.IntervalMin)*time.Minute {
		return
	}
	orch, err := m.store.TeamOrchestrator(ctx, project)
	if err != nil {
		return // no orchestrator: nobody to tell
	}
	// The recipient is checked again here (#4176 rule 18): one who left the team gets nothing,
	// and the check goes to the orchestrator instead.
	recipient := orch
	if hs.Recipient != "" {
		if sub, err := m.store.GetProjectSubscription(ctx, project, hs.Recipient); err == nil && sub != nil {
			recipient = sub.SubscriberID
		} else {
			m.logger.Warn("health check recipient is not a member of the team any more; sending to the orchestrator", "project", project)
		}
	}
	rcptSub, err := m.store.GetProjectSubscription(ctx, project, recipient)
	if err != nil || rcptSub == nil {
		return
	}
	rcptAgent, live := bySession[rcptSub.SessionName]
	if !live {
		return
	}
	text, items, err := m.composeCheck(ctx, project, orch, rcptSub, bySession)
	if err != nil || (items == 0 && !hs.AllClear) {
		return
	}
	if err := m.deliver(ctx, rcptAgent, "health-check:"+project, text); err != nil {
		m.logger.Warn("health check delivery failed", "project", project, "error", err)
		return
	}
	m.mu.Lock()
	m.lastCheck[project] = m.now()
	m.mu.Unlock()
}

// composeCheck gathers a board's items and writes its check: what Coral's own rules escalated, idle agents with no
// work, a reviewer's growing queue, queue problems; what the orchestrator acknowledged left out while unchanged; then
// the progress since the last check, the Operator's open questions and the roster. Its count is the items plus what
// the roster calls for attention.
func (m *BoardHealthMonitor) composeCheck(ctx context.Context, project, orch string, rcpt *board.Subscriber, bySession map[string]AgentInfo) (string, int, error) {
	now := m.now()
	subs, err := m.store.ListSubscribers(ctx, project)
	if err != nil {
		return "", 0, err
	}
	all, err := m.store.ListOpenTasks(ctx, project)
	if err != nil {
		return "", 0, err
	}
	tasks, parked, backFromPark := m.splitParked(ctx, project, all, now)
	waits, _ := m.store.ListActiveWaits(ctx, project)
	waiting := map[string]bool{}
	for _, w := range waits {
		waiting[w.SubscriberID] = true
	}

	items := m.escalationItems(ctx, project, parked, now)
	reviews, _ := m.store.OpenReviews(ctx, project)
	reviewers, _ := m.store.Reviewers(ctx, project)
	idle, agents, idleNoWork := m.idleItems(ctx, subs, bySession, orch, busyAgents(tasks), waiting, now)
	items = append(items, idle...)
	items = append(items, reviewItems(reviews, reviewers, parked, idleNoWork)...)
	// 4. Queue problems (task numbers only).
	issues, _, _ := taskQueueIssues(tasks)
	for _, is := range issues {
		items = append(items, teamhealth.Item{Rank: 4, Agent: "Board", Problem: is, Key: "queue|" + is, State: "shown"})
	}
	items = m.dropAcknowledged(ctx, project, items, now)

	m.mu.Lock()
	since, ok := m.lastCheck[project]
	m.mu.Unlock()
	if !ok {
		since = now.Add(-m.interval)
	}
	p, _ := m.store.ProgressSince(ctx, project, since)
	questions := m.operatorQuestions(ctx, project)
	roster, attention := m.buildRoster(ctx, rosterInputs{project: project, recipient: rcpt, subs: subs, bySession: bySession, waits: waits, tasks: tasks, now: now})
	return teamhealth.Compose(teamhealth.CheckInput{Now: now, Agents: agents, Items: items, Questions: questions,
		Progress: teamhealth.Progress{Since: since, Completed: p.Completed, Reviewed: p.Reviewed, Posts: p.Posts},
		Parked:   len(parked), BackFromPark: backFromPark, Roster: roster}), len(items) + attention, nil
}

// splitParked leaves parked tasks out: a parked task raises no item (#4175 step 3). A task whose park ended is back:
// said once in this check, and its park cleared now; cleared before the check is delivered, so a check that then
// fails to go out loses this note (a notice only: the task itself is back on the list either way).
func (m *BoardHealthMonitor) splitParked(ctx context.Context, project string, all []board.Task, now time.Time) (tasks []board.Task, parked map[string]bool, backFromPark []int64) {
	parked = map[string]bool{}
	for _, t := range all {
		if t.Parked(now) {
			parked[fmt.Sprint(t.ID)] = true
			continue
		}
		if t.ParkedReason != nil {
			backFromPark = append(backFromPark, t.ID)
			_, _ = m.store.UnparkTask(ctx, project, t.ID)
		}
		tasks = append(tasks, t)
	}
	return tasks, parked, backFromPark
}

// escalationItems: 1. what Coral's own rules escalated (a claim nudge, a status nudge), with what the agent last
// said for a status one.
func (m *BoardHealthMonitor) escalationItems(ctx context.Context, project string, parked map[string]bool, now time.Time) []teamhealth.Item {
	esc, err := m.store.HealthEscalations(ctx, project)
	if err != nil {
		return nil
	}
	said := func(name string) string {
		content, _, err := m.store.LastPostBy(ctx, project, name)
		if err != nil || content == "" {
			return ""
		}
		return teamhealth.ShownText(content, nil)
	}
	var items []teamhealth.Item
	for _, e := range esc {
		if parked[e.Subject] {
			continue
		}
		at, _ := time.Parse("2006-01-02T15:04:05.000Z", e.At)
		ago := teamhealth.Age(now.Sub(at))
		key := "escalation|" + e.Rule + "|" + e.Target + "|" + e.Subject
		switch e.Rule {
		case ruleClaimNudge:
			items = append(items, teamhealth.Item{Rank: 1, Agent: teamhealth.Name(e.Target),
				Problem: fmt.Sprintf("has not claimed #%s after Coral's claim nudges (escalated %s ago)", e.Subject, ago),
				Suggest: fmt.Sprintf("ask it, or reassign #%s", e.Subject), Key: key, State: e.At})
		case ruleStatusNudge:
			items = append(items, teamhealth.Item{Rank: 1, Agent: teamhealth.Name(e.Target),
				Problem: fmt.Sprintf("silent on #%s since Coral asked for a status (escalated %s ago)", e.Subject, ago),
				Said:    said(e.Target), Suggest: fmt.Sprintf("check on it, or reassign #%s", e.Subject), Key: key, State: e.At})
		}
	}
	return items
}

// busyAgents: the assignees of a task in progress or pending.
func busyAgents(tasks []board.Task) map[string]bool {
	busy := map[string]bool{}
	for _, t := range tasks {
		if t.AssignedTo != nil && (t.Status == "in_progress" || t.Status == "pending") {
			busy[*t.AssignedTo] = true
		}
	}
	return busy
}

// idleItems: 2. a running agent (not the orchestrator) with nothing in progress or assigned, not waiting, idle by
// its hook events for idleAfter: it needs work. It also counts the running agents, and returns who is idle with no
// work (for the review rule).
func (m *BoardHealthMonitor) idleItems(ctx context.Context, subs []board.Subscriber, bySession map[string]AgentInfo, orch string, busy, waiting map[string]bool, now time.Time) (items []teamhealth.Item, agents int, idleNoWork map[string]bool) {
	idleNoWork = map[string]bool{}
	for _, sub := range subs {
		a, live := bySession[sub.SessionName]
		if !live {
			continue
		}
		agents++
		if board.SameIdentity(sub.SubscriberID, orch) || busy[sub.SubscriberID] || waiting[sub.SubscriberID] {
			continue
		}
		sig, known := m.signal(ctx, a.SessionID)
		if !known || sig.InTurn || sig.NeedsInput || sig.Since.IsZero() || now.Sub(sig.Since) < m.idleAfter {
			continue
		}
		idleNoWork[sub.SubscriberID] = true
		items = append(items, teamhealth.Item{Rank: 2, Agent: teamhealth.Name(sub.SubscriberID),
			Problem: fmt.Sprintf("idle %s with nothing in progress or assigned", teamhealth.Age(now.Sub(sig.Since))),
			Suggest: "assign it the next task", Key: "idle|" + sub.SubscriberID, State: sig.Since.UTC().Format(time.RFC3339)})
	}
	return items, agents, idleNoWork
}

// reviewItems: 3. a reviewer with three or more reviews waiting (parked tasks left out) while another reviewer is
// idle with no work.
func reviewItems(reviews map[string][]int64, reviewers map[string]bool, parked, idleNoWork map[string]bool) []teamhealth.Item {
	var names []string
	for r := range reviews {
		names = append(names, r)
	}
	sort.Strings(names)
	var items []teamhealth.Item
	for _, r := range names {
		var open []int64
		for _, id := range reviews[r] {
			if !parked[fmt.Sprint(id)] {
				open = append(open, id)
			}
		}
		reviews[r] = open
		if len(reviews[r]) < 3 {
			continue
		}
		for other := range reviewers {
			if other != r && idleNoWork[other] {
				items = append(items, teamhealth.Item{Rank: 3, Agent: teamhealth.Name(r),
					Problem: fmt.Sprintf("%d reviews waiting (%s) while %s is idle", len(reviews[r]), taskList(reviews[r]), teamhealth.Name(other)),
					Suggest: fmt.Sprintf("move one to %s", teamhealth.Name(other)), Key: "reviews|" + r, State: fmt.Sprint(reviews[r])})
				break
			}
		}
	}
	return items
}

// dropAcknowledged leaves out what the orchestrator acknowledged while its state is the same (#4175 step 3): the
// states shown are recorded, acknowledged ones left out, and one whose acknowledgement lapsed is marked back.
func (m *BoardHealthMonitor) dropAcknowledged(ctx context.Context, project string, items []teamhealth.Item, now time.Time) []teamhealth.Item {
	states := map[string]string{}
	for _, it := range items {
		if it.Key != "" {
			states[teamhealth.ItemID(it.Key)] = it.State
		}
	}
	acked, back, err := m.store.NoteHealthItems(ctx, project, states, now)
	if err != nil {
		return items
	}
	kept := items[:0]
	for _, it := range items {
		if it.Key == "" || !acked[teamhealth.ItemID(it.Key)] {
			it.Back = it.Key != "" && back[teamhealth.ItemID(it.Key)]
			kept = append(kept, it)
		}
	}
	return kept
}

// operatorQuestions: the Operator's open questions (waits on the Operator), the asker's text shown safely.
func (m *BoardHealthMonitor) operatorQuestions(ctx context.Context, project string) []teamhealth.Question {
	qs, err := m.store.ListOperatorQuestions(ctx, project)
	if err != nil {
		return nil
	}
	var questions []teamhealth.Question
	for _, q := range qs {
		at, err := time.Parse(time.RFC3339, q.CreatedAt)
		if err != nil {
			continue
		}
		questions = append(questions, teamhealth.Question{Asker: teamhealth.Name(q.SubscriberID),
			Text: teamhealth.ShownText(q.Reason, nil), Since: at})
	}
	return questions
}

func taskList(ids []int64) string {
	out := ""
	for i, id := range ids {
		if i == 3 {
			return out + ", ..."
		}
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("#%d", id)
	}
	return out
}

// taskQueueIssues reports queue problems among a board's open tasks, the
// in-progress count per assignee, and the number of assigned pending tasks.
func taskQueueIssues(tasks []board.Task) (issues []string, assigned map[string]int, planned int) {
	assigned = map[string]int{}
	parallel := 0
	for _, t := range tasks {
		// Queued assignments are intentional landing slots; only active work
		// contributes to execution-load imbalance.
		if t.AssignedTo != nil && *t.AssignedTo != "" && t.Status == "in_progress" {
			assigned[*t.AssignedTo]++
		}
		if t.AssignedTo != nil && *t.AssignedTo != "" && t.Status == "pending" {
			planned++
		}
		// Assigned pending tasks already have an owner and may be deliberately
		// serialized. Recommend parallelization only for unassigned candidates.
		if t.Status == "pending" && t.AssignedTo == nil && len(t.BlockedBy) == 0 {
			parallel++
		}
		if t.Status == "blocked" {
			if len(t.BlockedBy) == 0 {
				issues = append(issues, fmt.Sprintf("#%d is blocked but has no prerequisites; unblock or cancel it", t.ID))
				continue
			}
			// "skipped" is the board's canceled state (as in #2916's
			// availabilityHealthReport fix).
			allDone, canceled := true, false
			for _, d := range t.BlockedBy {
				if d.Status != "completed" && d.Status != "skipped" {
					allDone = false
				}
				if d.Status == "skipped" {
					canceled = true
				}
			}
			if allDone {
				issues = append(issues, fmt.Sprintf("#%d is blocked but all prerequisites are resolved; refresh its dependency state", t.ID))
			}
			if canceled {
				issues = append(issues, fmt.Sprintf("#%d depends on canceled work; rewire or cancel it", t.ID))
			}
		}
	}
	if len(assigned) > 1 {
		vals := make([]int, 0, len(assigned))
		for _, n := range assigned {
			vals = append(vals, n)
		}
		sort.Ints(vals)
		if vals[len(vals)-1] >= vals[0]+2 {
			issues = append(issues, "assignment load is imbalanced across agents")
		}
	}
	if parallel > 1 {
		issues = append(issues, fmt.Sprintf("%d unblocked tasks can potentially run in parallel", parallel))
	}
	return issues, assigned, planned
}
