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
	store    *board.Store
	interval time.Duration
	mu       sync.Mutex
	last     map[string]string
}

func NewBoardHealthMonitor(store *board.Store, interval time.Duration) *BoardHealthMonitor {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &BoardHealthMonitor{store: store, interval: interval, last: make(map[string]string)}
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
		return
	}
	report := "[Coral board health] @Orchestrator\n" + strings.Join(issues, "\n- ")
	if len(subs) == 0 {
		return
	}
	m.mu.Lock()
	if m.last[project] == report {
		m.mu.Unlock()
		return
	}
	m.last[project] = report
	m.mu.Unlock()
	_, _ = m.store.PostMessage(ctx, project, "Coral Health Monitor", report, nil)
}
