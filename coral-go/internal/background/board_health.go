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
	idleAfter     time.Duration
	escalateAfter time.Duration
	mu            sync.Mutex
	last          map[string]string
	lastIssue     map[string]string
	idleNotified  map[int64]time.Time
	now           func() time.Time
	postMessage   func(context.Context, string, string, string, *string) (*board.Message, error)
}

func NewBoardHealthMonitor(store *board.Store, interval time.Duration) *BoardHealthMonitor {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &BoardHealthMonitor{store: store, interval: interval, idleAfter: 30 * time.Minute, escalateAfter: 60 * time.Minute, last: make(map[string]string), lastIssue: make(map[string]string), idleNotified: make(map[int64]time.Time), now: time.Now, postMessage: store.PostSystemMessage}
}

// SetRuntime is retained for callers that share a monitor runtime. Health
// monitoring never sends terminal input or captures terminal output.
func (m *BoardHealthMonitor) SetRuntime(_ AgentRuntime) {}

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

// boardHealthForgetAfter is how long the monitor remembers it already reported a task: past it, a
// task that is still idle is reported again and the memory does not grow with every task ever seen
// (#3999, the #3994 audit finding 5).
const boardHealthForgetAfter = 14 * 24 * time.Hour

// forgetOld drops reports older than boardHealthForgetAfter and the issue text of boards that are
// gone or have no open issue.
func (m *BoardHealthMonitor) forgetOld(now time.Time, projects map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, at := range m.idleNotified {
		if now.Sub(at) > boardHealthForgetAfter {
			delete(m.idleNotified, id)
		}
	}
	for key, v := range m.last {
		if at, err := time.Parse(time.RFC3339, v); err != nil || now.Sub(at) > boardHealthForgetAfter {
			delete(m.last, key)
		}
	}
	for project, issue := range m.lastIssue {
		if issue == "" || !projects[project] {
			delete(m.lastIssue, project)
		}
	}
}

func (m *BoardHealthMonitor) scan(ctx context.Context) {
	projects, err := m.store.ListProjects(ctx)
	if err != nil {
		return
	}
	known := make(map[string]bool, len(projects))
	for _, p := range projects {
		known[p.Project] = true
	}
	m.forgetOld(m.now(), known)
	for _, p := range projects {
		m.scanProject(ctx, p.Project)
	}
}

func (m *BoardHealthMonitor) scanProject(ctx context.Context, project string) {
	// Only open tasks matter here, and ListOpenTasks skips the per-task
	// workflow hydration ListTasks does for every task, completed ones included.
	tasks, err := m.store.ListOpenTasks(ctx, project)
	if err != nil {
		return
	}
	subs, err := m.store.ListSubscribers(ctx, project)
	if err != nil {
		return
	}
	issues, assigned, planned := taskQueueIssues(tasks)
	// What Coral's own rules escalated (#4175: an agent that didn't claim its work after the
	// claim nudges, or stayed silent after a status nudge); the orchestrator's check in step 2.
	if esc, err := m.store.HealthEscalations(ctx, project); err == nil {
		for _, e := range esc {
			switch e.Rule {
			case ruleClaimNudge:
				issues = append(issues, fmt.Sprintf("%s has not claimed task #%s after Coral's claim nudges", e.Target, e.Subject))
			case ruleStatusNudge:
				issues = append(issues, fmt.Sprintf("%s has been silent on task #%s since Coral asked for a status", e.Target, e.Subject))
			}
		}
	}
	recipients := orchestratorRecipients(subs)
	if len(recipients) == 0 {
		if len(issues) == 0 {
			m.mu.Lock()
			m.lastIssue[project] = ""
			m.mu.Unlock()
		}
		return
	}
	issueReport := ""
	if len(issues) > 0 {
		recipientTags := strings.Join(recipientMentions(recipients), " ")
		issueReport = "[Coral board health] " + recipientTags + "\n- " + strings.Join(issues, "\n- ")
	}
	m.mu.Lock()
	lastIssue := m.lastIssue[project]
	m.mu.Unlock()
	includeIssues := issueReport != "" && issueReport != lastIssue
	idleFindings := m.collectIdleFindings(ctx, project, recipients)
	report := composeHealthReport(issueReport, includeIssues, assigned, planned, recipients, idleFindings)
	if report == "" {
		if issueReport == "" {
			m.mu.Lock()
			m.lastIssue[project] = ""
			m.mu.Unlock()
		}
		return
	}
	if _, err := m.postMessage(ctx, project, "Coral Health Monitor", report, nil); err != nil {
		return
	}
	m.mu.Lock()
	if includeIssues {
		m.lastIssue[project] = issueReport
	} else if issueReport == "" {
		m.lastIssue[project] = ""
	}
	for _, finding := range idleFindings {
		if finding.mark != nil {
			finding.mark()
		}
	}
	m.mu.Unlock()
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

type idleFinding struct {
	text string
	mark func()
}

func composeHealthReport(issueReport string, includeIssues bool, assigned map[string]int, planned int, recipients []string, idleFindings []idleFinding) string {
	sections := make([]string, 0, 3)
	if includeIssues {
		sections = append(sections, issueReport)
	}
	if len(assigned) > 0 {
		owners := make([]string, 0, len(assigned))
		total := 0
		for owner, count := range assigned {
			owners = append(owners, owner)
			total += count
		}
		sort.Strings(owners)
		parts := make([]string, 0, len(owners))
		for _, owner := range owners {
			parts = append(parts, fmt.Sprintf("%s (%d)", owner, assigned[owner]))
		}
		sections = append(sections, fmt.Sprintf("[Coral team status] %s\nactive work: %d in_progress task(s) across %d agent(s)\nplanned work: %d queued assignment(s), excluded from active counts\n- %s", strings.Join(recipientMentions(recipients), " "), total, len(owners), planned, strings.Join(parts, ", ")))
	}
	for _, finding := range idleFindings {
		sections = append(sections, finding.text)
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n") + "\nReview current assignments, progress, and blockers, then take the next appropriate coordination action."
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

func (m *BoardHealthMonitor) collectIdleFindings(ctx context.Context, project string, recipients []string) []idleFinding {
	if len(recipients) == 0 {
		return nil
	}
	now := m.now()
	reminderBefore := now.Add(-m.idleAfter)
	tasks, err := m.store.IdleTasks(ctx, project, reminderBefore)
	if err != nil {
		return nil
	}
	findings := make([]idleFinding, 0)
	recipientTags := strings.Join(recipientMentions(recipients), " ")
	for _, task := range tasks {
		if task.AssignedTo == nil || *task.AssignedTo == "" {
			continue
		}
		m.mu.Lock()
		_, seen := m.idleNotified[task.ID]
		m.mu.Unlock()
		if !seen {
			findings = append(findings, idleFinding{text: fmt.Sprintf("%s [Task #%d idle] %s is still active but has had no recorded activity. Review its status, blocker, or completion update; use the task reminder snooze if tests or long-running work are active.", recipientTags, task.ID, task.Title), mark: func() { m.idleNotified[task.ID] = now }})
		}
	}
	escalationBefore := now.Add(-m.escalateAfter)
	escalationTasks, err := m.store.IdleTasks(ctx, project, escalationBefore)
	if err != nil {
		return findings
	}
	for _, task := range escalationTasks {
		if task.AssignedTo == nil || *task.AssignedTo == "" {
			continue
		}
		assignee := *task.AssignedTo
		key := fmt.Sprintf("escalated:%d", task.ID)
		m.mu.Lock()
		seen := m.last[key] != ""
		m.mu.Unlock()
		if seen {
			continue
		}
		findings = append(findings, idleFinding{text: fmt.Sprintf("%s [Task #%d stale] %s remains in_progress after an inactivity reminder to %s. Review status, blocker, or reassignment; Coral did not change ownership or completion.", recipientTags, task.ID, task.Title, assignee), mark: func() { m.last[key] = now.UTC().Format(time.RFC3339) }})
	}
	return findings
}
