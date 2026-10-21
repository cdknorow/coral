package background

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/teamhealth"
)

// TaskReminders sends each task's one-time reminder (task add --remind-at, #4175 step 3) when its
// time comes: to the task's assignee, or to the team's orchestrator when nobody is assigned,
// typed through the gate (held mid-turn, never into a dialog, #4176 rule 10), once. Its text and
// its board line hold the task number and the time only, never the title (rule 1).
type TaskReminders struct {
	store    *board.Store
	runtime  AgentRuntime
	logger   *slog.Logger
	discover func(ctx context.Context) ([]AgentInfo, error)
	deliver  func(ctx context.Context, a AgentInfo, key, text string) error
	now      func() time.Time
	Interval time.Duration
}

// NewTaskReminders checks for due reminders every 30 seconds.
func NewTaskReminders(bs *board.Store, rt AgentRuntime, discover func(context.Context) ([]AgentInfo, error)) *TaskReminders {
	t := &TaskReminders{store: bs, runtime: rt, logger: slog.Default(), discover: discover, now: time.Now, Interval: 30 * time.Second}
	t.deliver = func(ctx context.Context, a AgentInfo, key, text string) error {
		_, err := deliverAutomated(ctx, t.runtime, t.logger, naming.SessionName(a.AgentType, a.SessionID), a.AgentType, a.SessionID, key, board.OneLine(text), "task reminder", false, false)
		return err
	}
	return t
}

// Run sends due reminders every Interval until ctx is done.
func (t *TaskReminders) Run(ctx context.Context) error {
	return runEvery(ctx, t.Interval, t.Interval, func() { t.RunOnce(ctx) })
}

// ruleTaskReminder is the health action a task reminder takes.
const ruleTaskReminder = "task-reminder"

// taskReminderLimits are the board's limits for one agent and for the board (HealthNudges'
// PerTargetPerHour and TeamBudget).
var taskReminderLimits = board.HealthLimits{PerTargetPerHour: 3, TeamPerHour: 20}

func taskReminderText(id int64, at time.Time) string {
	return fmt.Sprintf("[Coral] Reminder for #%d, set for %s. Run 'coral-board task detail %d'.", id, at.Local().Format("15:04"), id)
}

// RunOnce sends the reminders due now.
func (t *TaskReminders) RunOnce(ctx context.Context) {
	now := t.now()
	due, err := t.store.DueTaskReminders(ctx, now)
	if err != nil || len(due) == 0 {
		return
	}
	agents, err := t.discover(ctx)
	if err != nil {
		return
	}
	bySession := map[string]AgentInfo{}
	for _, a := range agents {
		bySession[naming.SessionName(a.AgentType, a.SessionID)] = a
	}
	for _, task := range due {
		project := task.BoardID
		target := ""
		if task.AssignedTo != nil {
			target = *task.AssignedTo
		}
		if target == "" {
			if orch, err := t.store.TeamOrchestrator(ctx, project); err == nil {
				target = orch
			}
		}
		// Every reminder is an automatic action within the board's limits (Security's #4176 rule 5,
		// on #4225): 3 an hour per agent, 20 an hour per board. One refused stays unsent and goes
		// on a later pass, so a hundred tasks due at once can't flood one agent, and none is lost.
		if target != "" {
			if why, err := t.store.TakeHealthAction(ctx, project, ruleTaskReminder, target, now, taskReminderLimits); err != nil || why != "" {
				continue
			}
		}
		if ok, err := t.store.MarkTaskReminded(ctx, project, task.ID, now); err != nil || !ok {
			continue // another pass sent it
		}
		at, _ := time.Parse(time.RFC3339, *task.RemindAt)
		delivered := false
		if target != "" {
			if sub, err := t.store.GetProjectSubscription(ctx, project, target); err == nil && sub != nil {
				if a, live := bySession[sub.SessionName]; live {
					delivered = t.deliver(ctx, a, fmt.Sprintf("task-reminder:%s:%d", project, task.ID), taskReminderText(task.ID, at)) == nil
				}
			}
		}
		// The board keeps a record either way, so a reminder whose agent is not running is not lost.
		to := "nobody (no assignee, no orchestrator)"
		if target != "" {
			to = teamhealth.Name(target)
			if !delivered {
				to += " (not running: on the board only)"
			}
		}
		t.store.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf("[Task #%d reminder] set for %s, sent to %s", task.ID, at.Local().Format("2006-01-02 15:04"), to), nil)
	}
}
