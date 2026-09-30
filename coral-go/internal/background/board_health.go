package background

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/board"
)

// BoardHealthMonitor periodically audits task queues and sends a deduplicated
// report to each board's Orchestrator.
type BoardHealthMonitor struct {
	store         *board.Store
	interval      time.Duration
	runtime       AgentRuntime
	idleAfter     time.Duration
	escalateAfter time.Duration
	mu            sync.Mutex
	last          map[string]string
	idleNotified  map[int64]time.Time
	now           func() time.Time
	postMessage   func(context.Context, string, string, string, *string) (*board.Message, error)
}

func NewBoardHealthMonitor(store *board.Store, interval time.Duration) *BoardHealthMonitor {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &BoardHealthMonitor{store: store, interval: interval, idleAfter: 30 * time.Minute, escalateAfter: 60 * time.Minute, last: make(map[string]string), idleNotified: make(map[int64]time.Time), now: time.Now, postMessage: store.PostMessage}
}

// SetPostMessageFn provides an isolated delivery seam for retry tests.
func (m *BoardHealthMonitor) SetPostMessageFn(fn func(context.Context, string, string, string, *string) (*board.Message, error)) {
	if fn != nil {
		m.postMessage = fn
	}
}

// SetRuntime enables direct reminders to local assignees. The monitor still
// records board messages when no runtime is available.
func (m *BoardHealthMonitor) SetRuntime(runtime AgentRuntime) { m.runtime = runtime }

// SetIdleThresholds configures the first reminder and orchestrator escalation.
func (m *BoardHealthMonitor) SetIdleThresholds(remindAfter, escalateAfter time.Duration) {
	if remindAfter > 0 {
		m.idleAfter = remindAfter
	}
	if escalateAfter > 0 {
		m.escalateAfter = escalateAfter
	}
}

func (m *BoardHealthMonitor) Run(ctx context.Context) error {
	// Scan shortly after startup, then on the configured cadence.
	m.scan(ctx)
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
	projects, err := m.store.ListProjects(ctx)
	if err != nil {
		return
	}
	for _, p := range projects {
		m.scanProject(ctx, p.Project)
	}
}

func (m *BoardHealthMonitor) scanProject(ctx context.Context, project string) {
	tasks, err := m.store.ListTasks(ctx, project)
	if err != nil {
		return
	}
	subs, err := m.store.ListSubscribers(ctx, project)
	if err != nil {
		return
	}
	var issues []string
	assigned := map[string]int{}
	parallel := 0
	for _, t := range tasks {
		// Queued assignments are intentional landing slots; only active work
		// contributes to execution-load imbalance.
		if t.AssignedTo != nil && *t.AssignedTo != "" && t.Status == "in_progress" {
			assigned[*t.AssignedTo]++
		}
		// Assigned pending tasks already have an owner and may be deliberately
		// serialized. Recommend parallelization only for unassigned candidates.
		if t.Status == "pending" && t.AssignedTo == nil && len(t.BlockedBy) == 0 {
			parallel++
		}
		if t.Status == "blocked" {
			allDone, canceled := true, false
			for _, d := range t.BlockedBy {
				if d.Status != "completed" && d.Status != "skipped" {
					allDone = false
				}
				if d.Status == "canceled" {
					canceled = true
				}
			}
			if allDone {
				issues = append(issues, fmt.Sprintf("#%d is blocked but all prerequisites are complete; refresh its dependency state", t.ID))
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
	if len(issues) == 0 {
		m.scanIdleTasks(ctx, project)
		m.postActiveStatus(ctx, project, orchestratorRecipients(subs), assigned)
		return
	}
	recipients := orchestratorRecipients(subs)
	if len(recipients) == 0 {
		m.scanIdleTasks(ctx, project)
		return
	}
	recipientTags := strings.Join(recipientMentions(recipients), " ")
	report := "[Coral board health] " + recipientTags + "\n" + strings.Join(issues, "\n- ")
	emitted := false
	m.mu.Lock()
	duplicate := m.last[project] == report
	m.mu.Unlock()
	if !duplicate {
		if _, err := m.postMessage(ctx, project, "Coral Health Monitor", report, nil); err == nil {
			m.mu.Lock()
			m.last[project] = report
			m.mu.Unlock()
			emitted = true
		}
	}
	m.scanIdleTasks(ctx, project)
	if !emitted {
		m.postActiveStatus(ctx, project, recipients, assigned)
	}
}

// postActiveStatus emits one team summary per monitor cadence when work is
// active. Queued assignments are planned work; only in_progress tasks count.
func (m *BoardHealthMonitor) postActiveStatus(ctx context.Context, project string, recipients []string, assigned map[string]int) {
	if len(assigned) == 0 || len(recipients) == 0 {
		return
	}
	owners := make([]string, 0, len(assigned))
	total := 0
	for owner, count := range assigned {
		owners = append(owners, owner)
		total += count
	}
	sort.Strings(owners)
	parts := make([]string, 0, len(owners))
	for _, owner := range owners {
		// Assignee counts are display data, not notification recipients. Do not
		// prefix worker identities with @ or the board notifier will mention them.
		parts = append(parts, fmt.Sprintf("%s (%d)", owner, assigned[owner]))
	}
	report := fmt.Sprintf("[Coral team status] %s\nactive work: %d in_progress task(s) across %d agent(s)\n- %s", strings.Join(recipientMentions(recipients), " "), total, len(owners), strings.Join(parts, ", "))
	_, _ = m.postMessage(ctx, project, "Coral Health Monitor", report, nil)
}

func orchestratorRecipients(subs []board.Subscriber) []string {
	seen := make(map[string]bool)
	var recipients []string
	for _, sub := range subs {
		if sub.IsActive == 0 || (sub.CanPeek == 0 && !strings.EqualFold(strings.TrimSpace(sub.JobTitle), "Orchestrator")) || seen[sub.SubscriberID] {
			continue
		}
		seen[sub.SubscriberID] = true
		recipients = append(recipients, sub.SubscriberID)
	}
	sort.Strings(recipients)
	return recipients
}

func recipientMentions(recipients []string) []string {
	mentions := make([]string, len(recipients))
	for i, recipient := range recipients {
		mentions[i] = "@" + recipient
	}
	return mentions
}

func (m *BoardHealthMonitor) scanIdleTasks(ctx context.Context, project string) {
	now := m.now()
	reminderBefore := now.Add(-m.idleAfter)
	tasks, err := m.store.IdleTasks(ctx, project, reminderBefore)
	if err != nil {
		return
	}
	for _, task := range tasks {
		if task.AssignedTo == nil || *task.AssignedTo == "" {
			continue
		}
		assignee := *task.AssignedTo
		m.mu.Lock()
		_, seen := m.idleNotified[task.ID]
		m.mu.Unlock()
		if !seen {
			msg := fmt.Sprintf("[Task #%d idle] %s is still active but has had no recorded activity. Post a status, blocker, or completion update. Use the task reminder snooze if tests or long-running work are active.", task.ID, task.Title)
			_, _ = m.store.PostMessage(ctx, project, "Coral Health Monitor", "@"+assignee+" "+msg, nil)
			if sub, _ := m.store.GetProjectSubscription(ctx, project, assignee); sub != nil && m.runtime != nil && sub.SessionName != "" {
				_ = m.runtime.SendInput(ctx, sub.SessionName, msg)
			}
			m.mu.Lock()
			m.idleNotified[task.ID] = now
			m.mu.Unlock()
			continue
		}
	}
	escalationBefore := now.Add(-m.escalateAfter)
	escalationTasks, err := m.store.IdleTasks(ctx, project, escalationBefore)
	if err != nil {
		return
	}
	for _, task := range escalationTasks {
		if task.AssignedTo == nil || *task.AssignedTo == "" {
			continue
		}
		assignee := *task.AssignedTo
		key := fmt.Sprintf("escalated:%d", task.ID)
		m.mu.Lock()
		if m.last[key] != "" {
			m.mu.Unlock()
			continue
		}
		m.last[key] = now.UTC().Format(time.RFC3339)
		m.mu.Unlock()
		_, _ = m.store.PostMessage(ctx, project, "Coral Health Monitor", fmt.Sprintf("@Orchestrator [Task #%d stale] %s remains in_progress after an inactivity reminder to %s. Review status, blocker, or reassignment; Coral did not change ownership or completion.", task.ID, task.Title, assignee), nil)
	}
}
