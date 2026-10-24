// What a task's assignee and the orchestrators are told, and nudging a task (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/safego"
)

func (h *BoardHandler) buildAssignmentNotification(ctx context.Context, project string, task *board.Task, assignee string, reassigned bool) string {
	if task.Status == "blocked" {
		owner := "unassigned"
		if assignee != "" {
			owner = "@" + assignee
		}
		return fmt.Sprintf("[Task #%d assigned to %s; still blocked] %s — waiting for prerequisites; cannot be claimed yet", task.ID, owner, task.Title)
	}
	// A parked task is set aside on purpose: no one is invited to claim it, and no one is told it is theirs to start (#5183).
	if task.Parked(time.Now()) {
		why := ""
		if task.ParkedReason != nil {
			why = *task.ParkedReason
		}
		if assignee == "" {
			return fmt.Sprintf("[Task #%d now unassigned; parked, not for claiming yet: %s] %s", task.ID, why, task.Title)
		}
		return fmt.Sprintf("[Task #%d assigned to %s; parked, not to be started yet: %s] %s", task.ID, assignee, why, task.Title)
	}
	if assignee == "" {
		if reassigned {
			return fmt.Sprintf("@notify-all [Task #%d reassigned — now unassigned] %s — run 'coral-board task claim' to pick it up", task.ID, task.Title)
		}
		return fmt.Sprintf("@notify-all [New Task #%d (%s)] %s — run 'coral-board task claim' to pick it up", task.ID, task.Priority, task.Title)
	}

	hasActiveTask, err := h.bs.HasActiveTaskForAssignee(ctx, project, assignee, task.ID)
	if err != nil {
		slog.Warn("check active assignee task failed", "project", project, "assignee", assignee, "task_id", task.ID, "error", err)
	}
	if hasActiveTask {
		if reassigned {
			return fmt.Sprintf("[Task #%d reassigned to %s — notification deferred while they have an active task] %s", task.ID, assignee, task.Title)
		}
		return fmt.Sprintf("[Task #%d (%s) assigned to %s — notification deferred while they have an active task] %s", task.ID, task.Priority, assignee, task.Title)
	}

	if reassigned {
		return fmt.Sprintf("@%s [Task #%d reassigned to you] %s — run 'coral-board task claim %d' to start", assignee, task.ID, task.Title, task.ID)
	}
	return fmt.Sprintf("@%s [Task #%d (%s)] %s — assigned to you, run 'coral-board task claim %d' to start", assignee, task.ID, task.Priority, task.Title, task.ID)
}

func taskAssignedToSubscriber(task *board.Task, subscriberID string) bool {
	return task != nil && task.AssignedTo != nil && *task.AssignedTo == subscriberID
}

func isOrchestratorSubscriber(sub *board.Subscriber, subscriberID string) bool {
	if sub != nil && sub.CanPeek != 0 {
		return true
	}
	if strings.Contains(strings.ToLower(subscriberID), "orchestrator") {
		return true
	}
	if sub != nil && strings.Contains(strings.ToLower(sub.JobTitle), "orchestrator") {
		return true
	}
	return false
}

func (h *BoardHandler) notifyOrchestratorsTaskCompleted(ctx context.Context, project, subscriberID string, task *board.Task, msg string) {
	if h.terminal == nil || task == nil {
		return
	}
	subs, err := h.bs.ListSubscribers(ctx, project)
	if err != nil {
		slog.Warn("list subscribers for completion notification failed", "project", project, "task_id", task.ID, "error", err)
		return
	}
	// Same form as the board post; the chat view shows it as a Coral notice.
	notification := fmt.Sprintf("[Task #%d completed by %s] %s", task.ID, subscriberID, msg)
	for i := range subs {
		sub := &subs[i]
		if sub.SubscriberID == subscriberID || sub.SessionName == "" || !isOrchestratorSubscriber(sub, sub.SubscriberID) {
			continue
		}
		if err := h.sendAutomated(ctx, project, sub.SessionName, notification); err != nil {
			slog.Warn("failed to notify orchestrator of completed task", "orchestrator", sub.SubscriberID, "session", sub.SessionName, "task_id", task.ID, "error", err)
		}
	}
}

// notifyOrchestratorOfCancelledDependency reports downstream tasks whose
// non-termination dependency can no longer be satisfied after cancellation.
// The task remains blocked; the orchestrator must update or rewire it.
func (h *BoardHandler) notifyOrchestratorOfCancelledDependency(ctx context.Context, project, subscriberID string, cancelled *board.Task) {
	if cancelled == nil {
		return
	}
	downstream, err := h.bs.ListDownstreamTasks(ctx, project, cancelled.ID)
	if err != nil {
		slog.Warn("list downstream tasks for cancellation notification failed", "project", project, "task_id", cancelled.ID, "error", err)
		return
	}
	for i := range downstream {
		task := &downstream[i]
		deps, err := h.bs.GetTaskDependencies(ctx, task.ID)
		if err != nil {
			slog.Warn("load downstream dependencies for cancellation notification failed", "project", project, "task_id", task.ID, "error", err)
			continue
		}
		condition := ""
		for _, dep := range deps {
			if dep.TaskID == cancelled.ID && dep.BoardID == project && dep.Condition != "termination" && !dep.Satisfied {
				condition = dep.Condition
				break
			}
		}
		if condition == "" {
			continue
		}
		msg := fmt.Sprintf("[Task #%d stalled] %s depends on cancelled task #%d (%s); its %s dependency cannot be satisfied. Update or rewire the task.", task.ID, task.Title, cancelled.ID, cancelled.Title, condition)
		h.bs.PostSystemMessage(ctx, task.BoardID, "Coral Task Queue", "@Orchestrator "+msg, nil)
		if h.terminal == nil {
			continue
		}
		subs, err := h.bs.ListSubscribers(ctx, task.BoardID)
		if err != nil {
			slog.Warn("list subscribers for cancellation notification failed", "project", project, "task_id", task.ID, "error", err)
			continue
		}
		for j := range subs {
			sub := &subs[j]
			if sub.SubscriberID == subscriberID || sub.SessionName == "" || !isOrchestratorSubscriber(sub, sub.SubscriberID) {
				continue
			}
			if err := h.sendAutomated(ctx, task.BoardID, sub.SessionName, msg); err != nil {
				slog.Warn("failed to notify orchestrator of stalled task", "orchestrator", sub.SubscriberID, "session", sub.SessionName, "task_id", task.ID, "error", err)
			}
		}
	}
}

// sendTaskNudge types text into a subscriber's terminal, using their
// subscription on this board (the same name can be subscribed on others).
func (h *BoardHandler) sendTaskNudge(ctx context.Context, project, subscriberID, text string) error {
	if h.terminal == nil {
		return fmt.Errorf("no terminal available")
	}
	sub, err := h.bs.GetProjectSubscription(ctx, project, subscriberID)
	if err != nil {
		return err
	}
	if sub == nil || sub.SessionName == "" {
		return fmt.Errorf("%s has no running session on this board", subscriberID)
	}
	if h.isArchivedSession != nil && h.isArchivedSession(ctx, sub.SessionName) {
		return fmt.Errorf("%s is archived", subscriberID)
	}
	if err := h.sendAutomated(ctx, project, sub.SessionName, text); err != nil {
		slog.Warn("failed to nudge agent", "subscriber", subscriberID, "project", project, "session", sub.SessionName, "error", err)
		return err
	}
	return nil
}

// NudgeTask reminds a task's assignee about it in their terminal: a pending
// task is waiting for them, an in-progress one is still open. Nudges are
// typed into the agent's prompt, so one sent while the agent could not take
// input (e.g. while compacting) is lost; this lets the operator resend.
// POST /api/board/{project}/tasks/{taskID}/nudge
func (h *BoardHandler) NudgeTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	ctx := r.Context()
	task, err := h.bs.GetTask(ctx, project, taskID)
	if err != nil || task == nil {
		errNotFound(w, "Task not found")
		return
	}
	nudgeActor, attrOK := attributedTo(w, r, taskActor(r, ""))
	if !attrOK {
		return
	}
	if !h.requirePlannerOrAssignee(w, ctx, project, task, nudgeActor, "nudge its assignee") {
		return
	}
	assignee := ""
	if task.AssignedTo != nil {
		assignee = *task.AssignedTo
	}
	if assignee == "" {
		errBadRequest(w, "Task is not assigned to anyone")
		return
	}

	title := oneLineTitle(task.Title)
	if rs := []rune(title); len(rs) > 120 {
		title = string(rs[:119]) + "…"
	}
	var text string
	switch task.Status {
	case "pending":
		if hasActive, _ := h.bs.HasActiveTaskForAssignee(ctx, project, assignee, task.ID); hasActive {
			text = fmt.Sprintf("[Task #%d reminder] %s — assigned to you and waiting; claim it with 'coral-board task claim %d' when your current task is done.", task.ID, title, task.ID)
		} else {
			text = fmt.Sprintf("[Task #%d reminder] %s — assigned to you and waiting. Run 'coral-board task claim %d' to start.", task.ID, title, task.ID)
		}
	case "in_progress":
		text = fmt.Sprintf("[Task #%d reminder] %s — still in progress. Continue it, or run 'coral-board task complete %d' when it's done.", task.ID, title, task.ID)
	default:
		errBadRequest(w, fmt.Sprintf("Task #%d is %s; only pending and in-progress tasks can be nudged", task.ID, task.Status))
		return
	}
	if err := h.sendTaskNudge(ctx, project, assignee, text); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "assignee": assignee})
}

// SnoozeTaskReminder suppresses inactivity reminders for an active task.
// POST body: {"seconds": 1800}
func (h *BoardHandler) SnoozeTaskReminder(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		Seconds      int    `json:"seconds"`
		SubscriberID string `json:"subscriber_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Seconds <= 0 || body.Seconds > 7*24*60*60 {
		errBadRequest(w, "seconds must be between 1 and 604800")
		return
	}
	snoozeActor, attrOK := attributedTo(w, r, taskActor(r, body.SubscriberID))
	if !attrOK {
		return
	}
	if task, err := h.bs.GetTask(r.Context(), project, taskID); err != nil || task == nil {
		errNotFound(w, "Task not found")
		return
	} else if !h.requirePlannerOrAssignee(w, r.Context(), project, task, snoozeActor, "snooze its reminders") {
		return
	} else if task.Status != "in_progress" {
		errBadRequest(w, "only in-progress tasks can be snoozed")
		return
	}
	until := time.Now().Add(time.Duration(body.Seconds) * time.Second)
	if err := h.bs.SnoozeTaskReminder(r.Context(), project, taskID, until); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "snoozed_until": until.UTC().Format(time.RFC3339)})
}

// RemindTask starts or stops a server-side periodic reminder for a task.
// POST body: {"interval_seconds": 300}; DELETE stops the reminder.
func (h *BoardHandler) RemindTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	if r.Method == http.MethodDelete {
		task, err := h.bs.GetTask(r.Context(), project, taskID)
		if err != nil || task == nil {
			errNotFound(w, "Task not found")
			return
		}
		stopActor, attrOK := attributedTo(w, r, taskActor(r, ""))
		if !attrOK {
			return
		}
		if !h.requirePlannerOrAssignee(w, r.Context(), project, task, stopActor, "stop its reminders") {
			return
		}
		h.stopReminder(project, taskID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": false})
		return
	}
	var body struct {
		IntervalSeconds int    `json:"interval_seconds"`
		SubscriberID    string `json:"subscriber_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.IntervalSeconds < 30 || body.IntervalSeconds > 86400 {
		errBadRequest(w, "interval_seconds must be between 30 and 86400")
		return
	}
	task, err := h.bs.GetTask(r.Context(), project, taskID)
	if err != nil || task == nil {
		errNotFound(w, "Task not found")
		return
	}
	startActor, attrOK := attributedTo(w, r, taskActor(r, body.SubscriberID))
	if !attrOK {
		return
	}
	if !h.requirePlannerOrAssignee(w, r.Context(), project, task, startActor, "start its reminders") {
		return
	}
	if task.AssignedTo == nil || *task.AssignedTo == "" {
		errBadRequest(w, "Task is not assigned to anyone")
		return
	}
	if task.Status != "pending" && task.Status != "in_progress" {
		errBadRequest(w, fmt.Sprintf("Task #%d is %s; reminders require a pending or in-progress task", task.ID, task.Status))
		return
	}
	h.stopReminder(project, taskID)
	ctx, cancel := context.WithCancel(context.Background())
	h.reminderMu.Lock()
	h.reminders[h.reminderKey(project, taskID)] = cancel
	h.reminderMu.Unlock()
	interval := time.Duration(body.IntervalSeconds) * time.Second
	go func() {
		defer safego.Recover("server/routes/board:741")
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer h.stopReminder(project, taskID)
		for {
			select {
			case <-ticker.C:
				current, getErr := h.bs.GetTask(ctx, project, taskID)
				if getErr != nil || current == nil || current.AssignedTo == nil || (current.Status != "pending" && current.Status != "in_progress") {
					return
				}
				if current.Parked(time.Now()) {
					continue // parked: no reminder while it is (#4175 step 3)
				}
				_ = h.sendTaskNudge(ctx, project, *current.AssignedTo, fmt.Sprintf("[Task #%d reminder] %s — periodic reminder; run 'coral-board task current' or 'coral-board task claim'.", current.ID, oneLineTitle(current.Title)))
			case <-ctx.Done():
				return
			}
		}
	}()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": true, "interval_seconds": body.IntervalSeconds, "assignee": *task.AssignedTo})
}

// nudgeNextTask sends directly: Task Queue audit posts intentionally do not
// count as unread and cannot wake the worker through the generic notifier.
func (h *BoardHandler) nudgeNextTask(ctx context.Context, project, subscriberID string) {
	if busy, err := h.bs.HasActiveTaskForAssignee(ctx, project, subscriberID, 0); err != nil || busy {
		return
	}
	next := h.bs.NextPendingTaskForSubscriber(ctx, project, subscriberID)
	sub, err := h.bs.GetProjectSubscription(ctx, project, subscriberID)
	if err != nil || next == nil || (!taskAssignedToSubscriber(next, subscriberID) && isOrchestratorSubscriber(sub, subscriberID)) {
		return
	}
	audit := fmt.Sprintf("@%s You have tasks available — run 'coral-board task claim' to start", subscriberID)
	if taskAssignedToSubscriber(next, subscriberID) {
		audit = h.buildAssignmentNotification(ctx, project, next, subscriberID, false)
	}
	h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", audit, nil)
	text := taskNudge
	if taskAssignedToSubscriber(next, subscriberID) {
		text = taskClaimNudge(next.ID)
	}
	if err := h.sendTaskNudge(ctx, project, subscriberID, text); err != nil {
		slog.Warn("next task nudge failed", "project", project, "subscriber", subscriberID, "task_id", next.ID, "error", err)
	}
}
