package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/tracking"
)

const taskNudge = "You have tasks available. Run 'coral-board task claim' to start."

// BoardHandler handles message board HTTP endpoints.
type BoardHandler struct {
	bs                          *board.Store
	terminal                    ptymanager.SessionTerminal
	mu                          sync.RWMutex
	paused                      map[string]bool // in-memory set of paused project names
	notifyFn                    func()          // triggers immediate board notification pass
	coralDir                    string
	writeTaskArtifact           func(context.Context, *board.Task, string) error
	reminderMu                  sync.Mutex
	reminders                   map[string]context.CancelFunc
	subscriberReminderIntervals map[string]int
}

// SetTaskArtifactWriter configures persistence of task patches under .coral.
func (h *BoardHandler) SetTaskArtifactWriter(coralDir string, fn func(context.Context, *board.Task, string) error) {
	h.coralDir = coralDir
	h.writeTaskArtifact = fn
}

func (h *BoardHandler) taskArtifactPath(taskID int64) string {
	return filepath.Join(h.coralDir, "artifacts", "tasks", strconv.FormatInt(taskID, 10), "changes.diff")
}

func (h *BoardHandler) persistTaskArtifact(ctx context.Context, task *board.Task) {
	if task == nil || h.coralDir == "" || h.writeTaskArtifact == nil {
		return
	}
	if err := h.writeTaskArtifact(ctx, task, h.taskArtifactPath(task.ID)); err != nil {
		slog.Warn("persist task changes artifact failed", "task_id", task.ID, "board", task.BoardID, "error", err)
	}
}

func NewBoardHandler(bs *board.Store) *BoardHandler {
	return &BoardHandler{
		bs:                          bs,
		paused:                      make(map[string]bool),
		reminders:                   make(map[string]context.CancelFunc),
		subscriberReminderIntervals: make(map[string]int),
	}
}

func (h *BoardHandler) reminderKey(project string, taskID int64) string {
	return project + ":" + strconv.FormatInt(taskID, 10)
}

func (h *BoardHandler) stopReminder(project string, taskID int64) {
	h.reminderMu.Lock()
	if cancel := h.reminders[h.reminderKey(project, taskID)]; cancel != nil {
		cancel()
		delete(h.reminders, h.reminderKey(project, taskID))
	}
	h.reminderMu.Unlock()
}

// RemindSubscriber schedules a custom periodic instruction for a board agent.
// POST /api/board/{project}/reminder with subscriber_id, message and interval_seconds.
func (h *BoardHandler) RemindSubscriber(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	var body struct {
		SubscriberID    string `json:"subscriber_id"`
		Message         string `json:"message"`
		IntervalSeconds int    `json:"interval_seconds"`
	}
	if r.Method == http.MethodDelete {
		if err := decodeJSON(r, &body); err != nil {
			errBadRequest(w, "invalid JSON")
			return
		}
		h.stopSubscriberReminder(project, body.SubscriberID)
		_ = h.bs.DeleteSubscriberReminder(r.Context(), project, body.SubscriberID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": false})
		return
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.SubscriberID) == "" || strings.TrimSpace(body.Message) == "" {
		errBadRequest(w, "subscriber_id and message are required")
		return
	}
	if body.IntervalSeconds < 30 || body.IntervalSeconds > 86400 {
		errBadRequest(w, "interval_seconds must be between 30 and 86400")
		return
	}
	if sub, _ := h.bs.GetProjectSubscription(r.Context(), project, body.SubscriberID); sub == nil || sub.SessionName == "" {
		errBadRequest(w, "agent has no running session on this board")
		return
	}
	if err := h.bs.UpsertSubscriberReminder(r.Context(), board.SubscriberReminder{Project: project, SubscriberID: body.SubscriberID, Message: body.Message, IntervalSeconds: body.IntervalSeconds}); err != nil {
		errInternalServer(w, "could not persist reminder")
		return
	}
	h.startSubscriberReminder(project, body.SubscriberID, body.Message, time.Duration(body.IntervalSeconds)*time.Second)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": true, "subscriber_id": body.SubscriberID, "interval_seconds": body.IntervalSeconds})
}

// RestoreSubscriberReminders reloads durable reminders after a server restart.
func (h *BoardHandler) RestoreSubscriberReminders(ctx context.Context) error {
	rows, err := h.bs.ListSubscriberReminders(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if sub, _ := h.bs.GetProjectSubscription(ctx, r.Project, r.SubscriberID); sub == nil || sub.SessionName == "" {
			continue
		}
		h.startSubscriberReminder(r.Project, r.SubscriberID, r.Message, time.Duration(r.IntervalSeconds)*time.Second)
	}
	return nil
}

func (h *BoardHandler) subscriberReminderKey(project, subscriber string) string {
	return "agent:" + project + ":" + subscriber
}

func (h *BoardHandler) HasSubscriberReminder(project, subscriber string) bool {
	h.reminderMu.Lock()
	defer h.reminderMu.Unlock()
	return h.reminders[h.subscriberReminderKey(project, subscriber)] != nil
}
func (h *BoardHandler) SubscriberReminderInterval(project, subscriber string) (int, bool) {
	h.reminderMu.Lock()
	defer h.reminderMu.Unlock()
	seconds, ok := h.subscriberReminderIntervals[h.subscriberReminderKey(project, subscriber)]
	return seconds, ok && h.reminders[h.subscriberReminderKey(project, subscriber)] != nil
}
func (h *BoardHandler) stopSubscriberReminder(project, subscriber string) {
	h.reminderMu.Lock()
	if c := h.reminders[h.subscriberReminderKey(project, subscriber)]; c != nil {
		c()
		delete(h.reminders, h.subscriberReminderKey(project, subscriber))
	}
	delete(h.subscriberReminderIntervals, h.subscriberReminderKey(project, subscriber))
	h.reminderMu.Unlock()
}
func (h *BoardHandler) startSubscriberReminder(project, subscriber, message string, interval time.Duration) {
	h.stopSubscriberReminder(project, subscriber)
	ctx, cancel := context.WithCancel(context.Background())
	key := h.subscriberReminderKey(project, subscriber)
	h.reminderMu.Lock()
	if h.reminders == nil {
		h.reminders = make(map[string]context.CancelFunc)
	}
	if h.subscriberReminderIntervals == nil {
		h.subscriberReminderIntervals = make(map[string]int)
	}
	h.reminders[key] = cancel
	h.subscriberReminderIntervals[key] = int(interval / time.Second)
	h.reminderMu.Unlock()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer h.stopSubscriberReminder(project, subscriber)
		for {
			select {
			case <-ticker.C:
				if h.sendTaskNudge(ctx, project, subscriber, "[Coral reminder] "+message) != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// SetTerminal sets the terminal backend for peek functionality.
func (h *BoardHandler) SetTerminal(t ptymanager.SessionTerminal) {
	h.terminal = t
}

func (h *BoardHandler) isPaused(project string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.paused[project]
}

// SetPaused programmatically pauses or resumes a board (used by sleep/wake and startup).
func (h *BoardHandler) SetPaused(project string, paused bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if paused {
		h.paused[project] = true
	} else {
		delete(h.paused, project)
	}
}

// IsPaused returns whether a board is paused (exported for use by notifier).
func (h *BoardHandler) IsPaused(project string) bool {
	return h.isPaused(project)
}

// SetNotifyFn sets a callback that triggers an immediate board notification pass.
func (h *BoardHandler) SetNotifyFn(fn func()) {
	h.notifyFn = fn
}

func (h *BoardHandler) buildAssignmentNotification(ctx context.Context, project string, task *board.Task, assignee string, reassigned bool) string {
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
		return fmt.Sprintf("@%s [Task #%d reassigned to you] %s — run 'coral-board task claim' to start", assignee, task.ID, task.Title)
	}
	return fmt.Sprintf("@%s [Task #%d (%s)] %s — assigned to you, run 'coral-board task claim' to start", assignee, task.ID, task.Priority, task.Title)
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
		if err := h.terminal.SendInput(ctx, sub.SessionName, notification, "", ""); err != nil {
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
		h.bs.PostMessage(ctx, task.BoardID, "Coral Task Queue", "@Orchestrator "+msg, nil)
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
			if err := h.terminal.SendInput(ctx, sub.SessionName, msg, "", ""); err != nil {
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
	if err := h.terminal.SendInput(ctx, sub.SessionName, text, "", ""); err != nil {
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
	project := chi.URLParam(r, "project")
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
			text = fmt.Sprintf("[Task #%d reminder] %s — assigned to you and waiting; claim it with 'coral-board task claim' when your current task is done.", task.ID, title)
		} else {
			text = fmt.Sprintf("[Task #%d reminder] %s — assigned to you and waiting. Run 'coral-board task claim' to start.", task.ID, title)
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
	project := chi.URLParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		Seconds int `json:"seconds"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Seconds <= 0 || body.Seconds > 7*24*60*60 {
		errBadRequest(w, "seconds must be between 1 and 604800")
		return
	}
	if task, err := h.bs.GetTask(r.Context(), project, taskID); err != nil || task == nil {
		errNotFound(w, "Task not found")
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
	project := chi.URLParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	if r.Method == http.MethodDelete {
		h.stopReminder(project, taskID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": false})
		return
	}
	var body struct {
		IntervalSeconds int `json:"interval_seconds"`
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
				_ = h.sendTaskNudge(ctx, project, *current.AssignedTo, fmt.Sprintf("[Task #%d reminder] %s — periodic reminder; run 'coral-board task current' or 'coral-board task claim'.", current.ID, oneLineTitle(current.Title)))
			case <-ctx.Done():
				return
			}
		}
	}()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": true, "interval_seconds": body.IntervalSeconds, "assignee": *task.AssignedTo})
}

// unescapeLineBreaks turns literal "\n" sequences into line breaks in a
// message that has none. Agents posting through a shell often pass a
// single-quoted string with \n escapes, which arrives as one long line with
// the escapes shown verbatim. Two or more are required so a one-line message
// that mentions "\n" (e.g. strings.Split(s, "\n")) is left alone.
func unescapeLineBreaks(content string) string {
	if strings.ContainsAny(content, "\r\n") || strings.Count(content, `\n`) < 2 {
		return content
	}
	return strings.NewReplacer(`\r\n`, "\n", `\n`, "\n", `\t`, "\t").Replace(content)
}

// MarkRead moves a subscriber's read position forward to through_id after
// they looked at the newest messages (coral-board read --last N), so the
// unread count and nudges no longer include what they saw. from_id is the
// oldest message shown; the response counts unread messages before it that
// were skipped.
// POST /api/board/{project}/messages/mark-read {subscriber_id, through_id, from_id}
func (h *BoardHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		ThroughID    int64  `json:"through_id"`
		FromID       int64  `json:"from_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.SubscriberID == "" || body.ThroughID <= 0 {
		errBadRequest(w, "subscriber_id and through_id are required")
		return
	}
	if body.FromID <= 0 || body.FromID > body.ThroughID {
		body.FromID = body.ThroughID
	}
	skipped, first, last, err := h.bs.MarkReadThrough(r.Context(), project, body.SubscriberID, body.ThroughID, body.FromID)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skipped": skipped, "first_skipped_id": first, "last_skipped_id": last})
}

// ListProjects returns all boards with subscriber and message counts.
// GET /api/board/projects
func (h *BoardHandler) ListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.bs.ListProjects(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(projects))
}

// Subscribe subscribes to a board.
// POST /api/board/{project}/subscribe
// Accepts subscriber_id (stable identity) with optional session_name.
// Falls back to session_id for backwards compatibility.
func (h *BoardHandler) Subscribe(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	var body struct {
		SubscriberID string  `json:"subscriber_id"`
		SessionID    string  `json:"session_id"` // legacy compat
		SessionName  string  `json:"session_name"`
		JobTitle     string  `json:"job_title"`
		WebhookURL   *string `json:"webhook_url"`
		ReceiveMode  string  `json:"receive_mode"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	subscriberID := body.SubscriberID
	if subscriberID == "" {
		subscriberID = body.SessionID // legacy fallback
	}
	if subscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	if body.JobTitle == "" {
		body.JobTitle = "Agent"
	}
	sub, err := h.bs.Subscribe(r.Context(), project, subscriberID, body.JobTitle, body.SessionName, body.WebhookURL, nil, body.ReceiveMode)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// Unsubscribe removes a subscriber from a board.
// DELETE /api/board/{project}/subscribe
func (h *BoardHandler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		SessionID    string `json:"session_id"` // legacy compat
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	subscriberID := body.SubscriberID
	if subscriberID == "" {
		subscriberID = body.SessionID
	}
	if subscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	found, err := h.bs.Unsubscribe(r.Context(), project, subscriberID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !found {
		errNotFound(w, "subscriber not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PostMessage posts a message to a board.
// POST /api/board/{project}/messages
func (h *BoardHandler) PostMessage(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	var body struct {
		SubscriberID  string  `json:"subscriber_id"`
		SessionID     string  `json:"session_id"` // legacy compat
		Content       string  `json:"content"`
		TargetGroupID *string `json:"target_group_id,omitempty"`
		As            string  `json:"as,omitempty"` // display name for auto-subscribe (e.g. "Operator")
	}
	if err := decodeJSON(r, &body); err != nil || body.Content == "" {
		errBadRequest(w, "content required")
		return
	}
	body.Content = unescapeLineBreaks(body.Content)

	subscriberID := body.SubscriberID
	if subscriberID == "" {
		subscriberID = body.SessionID
	}

	// Auto-subscribe the poster if 'as' is provided and they aren't subscribed yet
	if body.As != "" && subscriberID != "" {
		sub, _ := h.bs.GetProjectSubscription(r.Context(), project, subscriberID)
		if sub == nil {
			h.bs.Subscribe(r.Context(), project, subscriberID, body.As, "", nil, nil, "all")
		}
	}

	msg, err := h.bs.PostMessage(r.Context(), project, subscriberID, body.Content, body.TargetGroupID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// A message from an assignee is evidence of progress on its active task.
	// Keep this separate from task state so posting never completes work.
	_ = h.bs.TouchActiveTask(r.Context(), project, subscriberID)

	// Fire-and-forget webhook dispatch
	go h.dispatchWebhooks(project, subscriberID, msg)

	// Trigger immediate board notification so subscribers get nudged right away
	if h.notifyFn != nil {
		h.notifyFn()
	}

	// Resolve any active registered waits for this message
	go h.resolveMessageWaits(project, subscriberID, body.Content)

	writeJSON(w, http.StatusOK, msg)
}

// dispatchWebhooks sends webhook callbacks to all subscribers with webhook_url set.
func (h *BoardHandler) dispatchWebhooks(project, senderSubscriberID string, msg *board.Message) {
	ctx := context.Background()
	targets, err := h.bs.GetWebhookTargets(ctx, project, senderSubscriberID)
	if err != nil || len(targets) == 0 {
		return
	}

	// Look up sender's job_title
	senderTitle := "Unknown"
	subs, err := h.bs.ListSubscribers(ctx, project)
	if err == nil {
		for _, s := range subs {
			if s.SubscriberID == senderSubscriberID {
				senderTitle = s.JobTitle
				break
			}
		}
	}

	payload := map[string]any{
		"project": project,
		"message": map[string]any{
			"id":            msg.ID,
			"subscriber_id": msg.SubscriberID,
			"job_title":     senderTitle,
			"content":       msg.Content,
			"created_at":    msg.CreatedAt,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	for _, target := range targets {
		if target.WebhookURL == nil || *target.WebhookURL == "" {
			continue
		}
		go func(url string) {
			req, err := http.NewRequest("POST", url, bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				slog.Debug("board webhook delivery failed", "url", url, "error", err)
				return
			}
			resp.Body.Close()
		}(*target.WebhookURL)
	}
}

// ReadMessages reads new messages (cursor-based).
// GET /api/board/{project}/messages
func (h *BoardHandler) ReadMessages(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	if h.isPaused(project) {
		writeJSON(w, http.StatusOK, []board.Message{})
		return
	}
	subscriberID := r.URL.Query().Get("subscriber_id")
	if subscriberID == "" {
		subscriberID = r.URL.Query().Get("session_id") // legacy compat
	}
	limit := queryInt(r, "limit", 50)
	allParam := r.URL.Query().Get("all")
	readAll := allParam == "true" || allParam == "1"
	messages, err := h.bs.ReadMessages(r.Context(), project, subscriberID, limit, readAll)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(messages))
}

// ListAllMessages returns all messages (no cursor advancement).
// GET /api/board/{project}/messages/all
// Pass ?id=N to fetch a single message by ID (does not advance cursor).
func (h *BoardHandler) ListAllMessages(w http.ResponseWriter, r *http.Request) {
	// Single message lookup by ID
	if idStr := r.URL.Query().Get("id"); idStr != "" {
		msgID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			errBadRequest(w, "invalid message id")
			return
		}
		msg, err := h.bs.GetMessageByID(r.Context(), msgID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "message not found"})
			return
		}
		writeJSON(w, http.StatusOK, []board.Message{*msg})
		return
	}

	project := chi.URLParam(r, "project")
	limit := queryInt(r, "limit", 200)
	if limit > 500 {
		limit = 500
	}
	offset := queryInt(r, "offset", 0)
	beforeID := int64(queryInt(r, "before_id", 0))
	format := r.URL.Query().Get("format")
	messages, err := h.bs.ListMessages(r.Context(), project, limit, offset, beforeID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if format == "dashboard" {
		total, _ := h.bs.CountMessages(r.Context(), project)
		writeJSON(w, http.StatusOK, map[string]any{
			"messages": messages,
			"total":    total,
			"limit":    limit,
			"offset":   offset,
		})
	} else {
		// Default: bare array for CLI consumers
		writeJSON(w, http.StatusOK, emptyIfNil(messages))
	}
}

// CheckUnread returns the unread message count.
// GET /api/board/{project}/messages/check
func (h *BoardHandler) CheckUnread(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	if h.isPaused(project) {
		writeJSON(w, http.StatusOK, map[string]any{"unread": 0})
		return
	}
	subscriberID := r.URL.Query().Get("subscriber_id")
	if subscriberID == "" {
		subscriberID = r.URL.Query().Get("session_id") // legacy compat
	}
	count, err := h.bs.CheckUnread(r.Context(), project, subscriberID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unread": count})
}

// DeleteMessage deletes a single message.
// DELETE /api/board/{project}/messages/{messageID}
func (h *BoardHandler) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	msgID, _ := strconv.ParseInt(chi.URLParam(r, "messageID"), 10, 64)
	found, err := h.bs.DeleteMessage(r.Context(), msgID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !found {
		errNotFound(w, "message not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ListSubscribers returns subscribers for a board.
// GET /api/board/{project}/subscribers
func (h *BoardHandler) ListSubscribers(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	subs, err := h.bs.ListSubscribers(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(subs))
}

// PeekAgent captures terminal output of another agent on the same board.
// GET /api/board/{project}/peek?target=<name>&subscriber_id=<caller>&lines=30
func (h *BoardHandler) PeekAgent(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	callerID := r.URL.Query().Get("subscriber_id")
	target := r.URL.Query().Get("target")
	lines := queryInt(r, "lines", 30)

	if lines > 500 {
		lines = 500
	}

	if callerID == "" || target == "" {
		errBadRequest(w, "subscriber_id and target are required")
		return
	}

	// Check caller has can_peek permission — use project-scoped lookup
	// to prevent cross-board authorization bypass.
	allSubs, err := h.bs.ListSubscribers(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	// Find caller in this board's subscribers
	var caller *board.Subscriber
	for i, s := range allSubs {
		if s.SubscriberID == callerID {
			caller = &allSubs[i]
			break
		}
	}
	if caller == nil {
		errForbidden(w, "not subscribed to this board")
		return
	}
	if caller.CanPeek == 0 {
		errForbidden(w, "peek permission not granted for this subscriber")
		return
	}

	// Find target in same board's subscribers
	var targetSub *board.Subscriber
	for i, s := range allSubs {
		if s.SubscriberID == target || s.JobTitle == target {
			targetSub = &allSubs[i]
			break
		}
	}
	if targetSub == nil {
		errNotFound(w, "target subscriber not found on this board")
		return
	}
	_ = h.bs.TouchActiveTask(r.Context(), project, targetSub.SubscriberID)

	if h.terminal == nil {
		errInternalServer(w, "terminal backend not available")
		return
	}

	// Capture terminal output using the target's session name
	output, err := h.terminal.CaptureOutput(r.Context(), targetSub.SessionName, lines, "", "")
	if err != nil {
		slog.Warn("peek capture failed", "target", target, "error", err)
		errInternalServer(w, "failed to capture terminal output")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"target":       target,
		"session_name": targetSub.SessionName,
		"lines":        lines,
		"output":       output,
	})
}

// PauseBoard pauses reads for a board.
// POST /api/board/{project}/pause
func (h *BoardHandler) PauseBoard(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	h.mu.Lock()
	h.paused[project] = true
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": true})
}

// ResumeBoard resumes reads for a board.
// POST /api/board/{project}/resume
func (h *BoardHandler) ResumeBoard(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	h.mu.Lock()
	delete(h.paused, project)
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": false})
}

// GetPaused returns whether a board is paused.
// GET /api/board/{project}/paused
func (h *BoardHandler) GetPaused(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	writeJSON(w, http.StatusOK, map[string]any{"paused": h.isPaused(project)})
}

// DeleteBoard deletes a board and all its messages.
// DELETE /api/board/{project}
func (h *BoardHandler) DeleteBoard(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	h.mu.Lock()
	delete(h.paused, project)
	h.mu.Unlock()
	h.bs.DeleteProject(r.Context(), project)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Group endpoints ──────────────────────────────────────────────────

// ListGroups returns all groups for a project with member counts.
// GET /api/board/{project}/groups
func (h *BoardHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	groups, err := h.bs.ListGroups(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(groups))
}

// ListGroupMembers returns subscriber IDs in a group.
// GET /api/board/{project}/groups/{groupID}/members
func (h *BoardHandler) ListGroupMembers(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	groupID := chi.URLParam(r, "groupID")
	members, err := h.bs.ListGroupMembers(r.Context(), project, groupID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(members))
}

// AddGroupMember adds a subscriber to a group.
// POST /api/board/{project}/groups/{groupID}/members
func (h *BoardHandler) AddGroupMember(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	groupID := chi.URLParam(r, "groupID")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		SessionID    string `json:"session_id"` // legacy compat
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	subscriberID := body.SubscriberID
	if subscriberID == "" {
		subscriberID = body.SessionID
	}
	if subscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	if err := h.bs.AddToGroup(r.Context(), project, groupID, subscriberID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveGroupMember removes a subscriber from a group.
// DELETE /api/board/{project}/groups/{groupID}/members/{subscriberID}
func (h *BoardHandler) RemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	groupID := chi.URLParam(r, "groupID")
	subscriberID := chi.URLParam(r, "sessionID") // URL param name kept for route compat
	removed, err := h.bs.RemoveFromGroup(r.Context(), project, groupID, subscriberID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !removed {
		errNotFound(w, "member not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Task endpoints ───────────────────────────────────────────────────

// CreateTask creates a new task on a board.
// POST /api/board/{project}/tasks
func (h *BoardHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	var body struct {
		Title        string             `json:"title"`
		Body         string             `json:"body"`
		Priority     string             `json:"priority"`
		CreatedBy    string             `json:"created_by"`
		SubscriberID string             `json:"subscriber_id"`
		AssignedTo   string             `json:"assigned_to"`
		BlockedBy    json.RawMessage    `json:"blocked_by,omitempty"`
		Draft        bool               `json:"draft,omitempty"`
		Workflow     board.TaskWorkflow `json:"workflow,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Title == "" {
		errBadRequest(w, "title required")
		return
	}
	createdBy := body.CreatedBy
	if createdBy == "" {
		createdBy = body.SubscriberID
	}
	if createdBy == "" {
		errBadRequest(w, "created_by or subscriber_id required")
		return
	}
	if body.Priority == "" {
		body.Priority = "medium"
	}

	opts := &board.CreateTaskOpts{Draft: body.Draft, Workflow: body.Workflow}
	if len(body.BlockedBy) > 0 {
		deps, err := parseBlockedBy(body.BlockedBy, project)
		if err != nil {
			errBadRequest(w, err.Error())
			return
		}
		opts.BlockedBy, opts.MaxDepth = deps, 32
	}

	task, err := h.bs.CreateTaskWithOpts(r.Context(), project, body.Title, body.Body, body.Priority, createdBy, opts, body.AssignedTo)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}

	assignee := ""
	if task.AssignedTo != nil {
		assignee = *task.AssignedTo
	}
	if task.Status == "draft" {
		go func() {
			notification := fmt.Sprintf("[Task #%d (draft)] %s", task.ID, task.Title)
			h.bs.PostMessage(context.Background(), project, "Coral Task Queue", notification, nil)
		}()
	} else if task.Status == "blocked" {
		go func() {
			deps, _ := h.bs.GetTaskDependencies(context.Background(), task.ID)
			blockerList := formatBlockerList(deps)
			notification := fmt.Sprintf("[Task #%d (blocked)] %s — blocked by %s", task.ID, task.Title, blockerList)
			h.bs.PostMessage(context.Background(), project, "Coral Task Queue", notification, nil)
		}()
	} else {
		go func() {
			ctx := context.Background()
			notification := h.buildAssignmentNotification(ctx, project, task, assignee, false)
			h.bs.PostMessage(ctx, project, "Coral Task Queue", notification, nil)

			// Send direct terminal nudge
			if h.terminal != nil {
				if assignee != "" {
					hasActive, _ := h.bs.HasActiveTaskForAssignee(ctx, project, assignee, task.ID)
					if !hasActive {
						h.sendTaskNudge(ctx, project, assignee, taskNudge)
					}
				} else {
					idle := h.bs.FindIdleSubscriber(ctx, project)
					if idle != nil && idle.SessionName != "" {
						if err := h.terminal.SendInput(ctx, idle.SessionName, taskNudge, "", ""); err != nil {
							slog.Warn("failed to nudge agent", "subscriber", idle.SubscriberID, "session", idle.SessionName, "error", err)
						}
					}
				}
			}
		}()
	}
	writeJSON(w, http.StatusCreated, task)
}

// parseBlockedBy handles both shorthand [1, 2, 3] and full [{task_id: 1, board_id: "x"}] formats.
func parseBlockedBy(raw json.RawMessage, defaultBoard string) ([]board.TaskDep, error) {
	// Try shorthand: array of integers
	var ids []int64
	if err := json.Unmarshal(raw, &ids); err == nil {
		deps := make([]board.TaskDep, len(ids))
		for i, id := range ids {
			deps[i] = board.TaskDep{TaskID: id, BoardID: defaultBoard}
		}
		return deps, nil
	}
	// Try full format: array of TaskDep objects
	var deps []board.TaskDep
	if err := json.Unmarshal(raw, &deps); err != nil {
		return nil, fmt.Errorf("invalid blocked_by format")
	}
	for i := range deps {
		if deps[i].BoardID == "" {
			deps[i].BoardID = defaultBoard
		}
	}
	return deps, nil
}

func formatBlockerList(deps []board.TaskDep) string {
	if len(deps) == 0 {
		return "unknown"
	}
	parts := make([]string, len(deps))
	for i, d := range deps {
		parts[i] = fmt.Sprintf("#%d", d.TaskID)
		if d.BlockedReason != "" {
			parts[i] += " (" + d.BlockedReason + ")"
		}
	}
	return strings.Join(parts, ", ")
}

// ListTasks returns all tasks for a board.
// GET /api/board/{project}/tasks
func (h *BoardHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	tasks, err := h.bs.ListTasks(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": emptyIfNil(tasks)})
}

// ListAllTasks returns the most recent tasks across all boards.
// GET /api/board/tasks?limit=100
func (h *BoardHandler) ListAllTasks(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	tasks, err := h.bs.ListAllTasks(r.Context(), limit)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": emptyIfNil(tasks)})
}

// ActiveTask returns the subscriber's current in-progress task.
func (h *BoardHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil || id <= 0 {
		errBadRequest(w, "invalid task ID")
		return
	}
	task, err := h.bs.GetTask(r.Context(), chi.URLParam(r, "project"), id)
	if err != nil {
		errNotFound(w, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// POST /api/board/{project}/tasks/current
func (h *BoardHandler) ActiveTask(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	task := h.bs.ActiveTaskForSubscriber(r.Context(), project, body.SubscriberID)
	if task == nil {
		errNotFound(w, "no active task")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// ClaimTask claims the next available task by priority.
// POST /api/board/{project}/tasks/claim
func (h *BoardHandler) ClaimTask(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		TaskID       int64  `json:"task_id,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	task, err := h.bs.ClaimTask(r.Context(), project, body.SubscriberID, body.TaskID)
	if err != nil {
		if err.Error() == "complete your current task before claiming a new one" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		} else {
			blocked, lookupErr := h.bs.BlockedTasksForSubscriber(r.Context(), project, body.SubscriberID, body.TaskID)
			if lookupErr != nil {
				errInternalServer(w, lookupErr.Error())
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "blocked_tasks": blocked})
		}
		return
	}
	if task == nil {
		blocked, err := h.bs.BlockedTasksForSubscriber(r.Context(), project, body.SubscriberID, body.TaskID)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no available tasks", "blocked_tasks": blocked})
		return
	}
	// Post board notification asynchronously to avoid DB contention
	go func() {
		notification := fmt.Sprintf("[Task #%d claimed by %s] %s", task.ID, body.SubscriberID, task.Title)
		h.bs.PostMessage(context.Background(), project, "Coral Task Queue", notification, nil)
	}()
	writeJSON(w, http.StatusOK, task)
}

// CompleteTaskByID marks a task as completed.
// POST /api/board/{project}/tasks/{taskID}/complete
func (h *BoardHandler) CompleteTaskByID(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		SubscriberID string               `json:"subscriber_id"`
		Message      *string              `json:"message"`
		Outcome      string               `json:"outcome,omitempty"`
		Artifacts    []board.TaskArtifact `json:"artifacts,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	task, err := h.bs.CompleteTaskWithArtifacts(r.Context(), project, taskID, body.SubscriberID, body.Message, body.Outcome, body.Artifacts)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	h.persistTaskArtifact(r.Context(), task)
	// Funnel milestone: the first board task ever completed on this install.
	tracking.TrackOnce(tracking.EventFirstTaskCompleted, nil)
	// Copy values for goroutine closure safety
	completedTask := task
	subscriberID := body.SubscriberID
	completionMsg := ""
	if body.Message != nil {
		completionMsg = *body.Message
	}
	if completedTask == nil {
		writeJSON(w, http.StatusOK, task)
		return
	}
	// Post board notification and send direct terminal nudge asynchronously
	go func() {
		ctx := context.Background()
		msg := completedTask.Title
		if completionMsg != "" {
			msg = completionMsg
		}
		notification := fmt.Sprintf("[Task #%d completed by %s] Outcome: %s; %d artifacts. %s", completedTask.ID, subscriberID, completedTask.Workflow.Outcome, len(completedTask.Workflow.Artifacts), msg)
		h.bs.PostMessage(ctx, project, "Coral Task Queue", notification, nil)
		h.notifyOrchestratorsTaskCompleted(ctx, project, subscriberID, completedTask, fmt.Sprintf("Outcome: %s. %s", completedTask.Workflow.Outcome, msg))

		// Resolve downstream blocked tasks
		h.notifyUnblockedTasks(ctx, project, completedTask.ID)

		// Resolve active task waits
		h.resolveTaskWaits(project, completedTask.ID, completedTask.Title, "completed")

		// Check if the agent has more pending tasks. Unassigned pool tasks nudge
		// workers, but not the orchestrator; explicitly assigned tasks still do.
		nextTask := h.bs.NextPendingTaskForSubscriber(ctx, project, subscriberID)
		sub, _ := h.bs.GetProjectSubscription(ctx, project, subscriberID)
		if nextTask != nil && (taskAssignedToSubscriber(nextTask, subscriberID) || !isOrchestratorSubscriber(sub, subscriberID)) {
			auditMsg := fmt.Sprintf("@%s You have tasks available — run 'coral-board task claim' to start",
				subscriberID)
			h.bs.PostMessage(ctx, project, "Coral Task Queue", auditMsg, nil)
			h.sendTaskNudge(ctx, project, subscriberID, taskNudge)
		}
	}()
	writeJSON(w, http.StatusOK, task)
}

// CancelTaskByID marks a task as skipped/cancelled.
// POST /api/board/{project}/tasks/{taskID}/cancel
func (h *BoardHandler) CancelTaskByID(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		SubscriberID string  `json:"subscriber_id"`
		Message      *string `json:"message"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	task, err := h.bs.CancelTask(r.Context(), project, taskID, body.SubscriberID, body.Message)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	h.persistTaskArtifact(r.Context(), task)
	stalled, stallErr := h.bs.StallDownstreamTasks(r.Context(), project, task.ID)
	if stallErr != nil {
		slog.Warn("failed to reconcile downstream tasks after cancellation", "project", project, "task_id", task.ID, "error", stallErr)
	}
	go func() {
		ctx := context.Background()
		notification := fmt.Sprintf("[Task #%d cancelled by %s] %s", task.ID, body.SubscriberID, task.Title)
		h.bs.PostMessage(ctx, project, "Coral Task Queue", notification, nil)
		// Reconcile the full descendant graph immediately. Pending descendants
		// become blocked/stalled and retain their owner for orchestrator repair.
		if stallErr == nil {
			for _, child := range stalled {
				h.bs.PostMessage(ctx, project, "Coral Task Queue", fmt.Sprintf("@Orchestrator [Task #%d stalled] %s depends on cancelled task #%d; rewire or create a retry before claiming it.", child.ID, child.Title, task.ID), nil)
			}
		}
		h.notifyOrchestratorOfCancelledDependency(ctx, project, body.SubscriberID, task)

		// Only termination dependencies are satisfied by cancellation.
		h.notifyUnblockedTasks(ctx, project, task.ID)

		// Resolve active task waits
		h.resolveTaskWaits(project, task.ID, task.Title, "cancelled")
	}()
	writeJSON(w, http.StatusOK, task)
}

// TaskChangesDiff serves the patch captured when a task completed or was skipped.
// GET /api/board/{project}/tasks/{taskID}/changes.diff
func (h *BoardHandler) TaskChangesDiff(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	tasks, err := h.bs.ListTasks(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	found := false
	for i := range tasks {
		if tasks[i].ID == taskID {
			found = true
			break
		}
	}
	if !found {
		errNotFound(w, "task not found")
		return
	}
	if h.coralDir == "" {
		errNotFound(w, "task changes artifact not found")
		return
	}
	data, err := os.ReadFile(h.taskArtifactPath(taskID))
	if err != nil {
		if os.IsNotExist(err) {
			errNotFound(w, "task changes artifact not found")
			return
		}
		errInternalServer(w, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="changes.diff"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// UpdateTask applies partial edits to a pending, in_progress, or blocked task.
// PATCH /api/board/{project}/tasks/{taskID}
func (h *BoardHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var raw struct {
		Title      *string         `json:"title,omitempty"`
		Body       *string         `json:"body,omitempty"`
		Priority   *string         `json:"priority,omitempty"`
		AssignedTo *string         `json:"assigned_to,omitempty"`
		BlockedBy  json.RawMessage `json:"blocked_by,omitempty"`
	}
	if err := decodeJSON(r, &raw); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}

	update := board.TaskUpdate{
		Title:      raw.Title,
		Body:       raw.Body,
		Priority:   raw.Priority,
		AssignedTo: raw.AssignedTo,
	}

	if len(raw.BlockedBy) > 0 {
		deps, err := parseBlockedBy(raw.BlockedBy, project)
		if err != nil {
			errBadRequest(w, err.Error())
			return
		}
		update.BlockedBy = &deps
	}

	before, _ := h.bs.GetTask(r.Context(), project, taskID)
	task, prevStatus, err := h.bs.UpdateTask(r.Context(), project, taskID, update, 32)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	_ = h.bs.TouchTask(r.Context(), project, taskID)
	go func() {
		ctx := context.Background()
		nudged := false
		notification := fmt.Sprintf("[Task #%d edited] %s", task.ID, task.Title)
		h.bs.PostMessage(ctx, project, "Coral Task Queue", notification, nil)

		// Notify on status transitions from dependency changes
		if prevStatus == "pending" && task.Status == "blocked" {
			assignee := ""
			if task.AssignedTo != nil {
				assignee = *task.AssignedTo
			}
			if assignee != "" {
				msg := fmt.Sprintf("[Task #%d blocked] %s — @%s task is now blocked, please pause work", task.ID, task.Title, assignee)
				h.bs.PostMessage(ctx, project, "Coral Task Queue", msg, nil)
			}
		} else if prevStatus == "blocked" && task.Status == "pending" {
			assignee := ""
			if task.AssignedTo != nil {
				assignee = *task.AssignedTo
			}
			msg := fmt.Sprintf("[Task #%d unblocked] %s", task.ID, task.Title)
			if assignee != "" {
				msg += fmt.Sprintf(" — @%s your task is now ready", assignee)
			}
			h.bs.PostMessage(ctx, project, "Coral Task Queue", msg, nil)

			if assignee != "" {
				h.sendTaskNudge(ctx, project, assignee, taskNudge)
				nudged = true
			}
		}
		if raw.AssignedTo != nil && task.Status == "pending" {
			previous := ""
			if before != nil && before.AssignedTo != nil {
				previous = *before.AssignedTo
			}
			current := ""
			if task.AssignedTo != nil {
				current = *task.AssignedTo
			}
			if !nudged && current != "" && current != previous {
				if hasActive, _ := h.bs.HasActiveTaskForAssignee(ctx, project, current, task.ID); !hasActive {
					h.sendTaskNudge(ctx, project, current, taskNudge)
				}
			}
		}
	}()
	writeJSON(w, http.StatusOK, task)
}

// ReassignTask resets a task to pending with an optional new assignee.
// POST /api/board/{project}/tasks/{taskID}/reassign
func (h *BoardHandler) ReassignTask(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		Assignee     string `json:"assignee"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	task, err := h.bs.ReassignTask(r.Context(), project, taskID, body.Assignee)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	go func() {
		ctx := context.Background()
		notification := h.buildAssignmentNotification(ctx, project, task, body.Assignee, true)
		h.bs.PostMessage(ctx, project, "Coral Task Queue", notification, nil)
		if body.Assignee != "" {
			if hasActive, _ := h.bs.HasActiveTaskForAssignee(ctx, project, body.Assignee, task.ID); !hasActive {
				h.sendTaskNudge(ctx, project, body.Assignee, taskNudge)
			}
		}

		// Reassigning resets to pending — re-block downstream tasks that depended on this one
		reblocked, _ := h.bs.ReblockDownstreamTasks(ctx, project, task.ID)
		for _, t := range reblocked {
			assignee := ""
			if t.AssignedTo != nil {
				assignee = *t.AssignedTo
			}
			msg := fmt.Sprintf("[Task #%d blocked] %s", t.ID, t.Title)
			if assignee != "" {
				msg += fmt.Sprintf(" — @%s task is blocked again, please pause work", assignee)
			}
			h.bs.PostMessage(ctx, t.BoardID, "Coral Task Queue", msg, nil)
		}
	}()
	writeJSON(w, http.StatusOK, task)
}

// PublishTask transitions a draft task to pending or blocked.
// POST /api/board/{project}/tasks/{taskID}/publish
func (h *BoardHandler) PublishTask(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	task, err := h.bs.PublishTask(r.Context(), project, taskID)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}

	assignee := ""
	if task.AssignedTo != nil {
		assignee = *task.AssignedTo
	}
	if task.Status == "blocked" {
		go func() {
			deps, _ := h.bs.GetTaskDependencies(context.Background(), task.ID)
			blockerList := formatBlockerList(deps)
			notification := fmt.Sprintf("[Task #%d published (blocked)] %s — blocked by %s", task.ID, task.Title, blockerList)
			h.bs.PostMessage(context.Background(), project, "Coral Task Queue", notification, nil)
		}()
	} else {
		go func() {
			ctx := context.Background()
			notification := fmt.Sprintf("[Task #%d published] %s", task.ID, task.Title)
			if assignee != "" {
				notification += fmt.Sprintf(" — @%s", assignee)
			}
			h.bs.PostMessage(ctx, project, "Coral Task Queue", notification, nil)

			if assignee != "" {
				hasActive, _ := h.bs.HasActiveTaskForAssignee(ctx, project, assignee, task.ID)
				if !hasActive {
					h.sendTaskNudge(ctx, project, assignee, taskNudge)
				}
			}
		}()
	}
	writeJSON(w, http.StatusOK, task)
}

// RecoverTaskNotifications delivers queued readiness notices after a restart.
// Terminal delivery remains best effort; persisted readiness does not depend on it.
func (h *BoardHandler) RecoverTaskNotifications(ctx context.Context) {
	if h.bs != nil {
		h.notifyUnblockedTasks(ctx, "", 0)
	}
}

// notifyUnblockedTasks consumes queued transitions and sends notifications + nudges.
func (h *BoardHandler) notifyUnblockedTasks(ctx context.Context, project string, completedTaskID int64) {
	unblocked, err := h.bs.ResolveDownstreamTasks(ctx, project, completedTaskID)
	if err != nil || len(unblocked) == 0 {
		return
	}
	for _, t := range unblocked {
		assignee := ""
		if t.AssignedTo != nil {
			assignee = *t.AssignedTo
		}
		msg := fmt.Sprintf("[Task #%d unblocked] %s", t.ID, t.Title)
		if assignee != "" {
			msg += fmt.Sprintf(" — @%s your task is now ready", assignee)
		}
		h.bs.PostMessage(ctx, t.BoardID, "Coral Task Queue", msg, nil)

		// Send terminal nudge to assignee
		if assignee != "" {
			h.sendTaskNudge(ctx, t.BoardID, assignee, taskNudge)
		}

		// Resolve active task waits
		h.resolveTaskWaits(t.BoardID, t.ID, t.Title, "unblocked")
	}
}

// TaskLiveCost returns real-time cost for a task by querying proxy_requests
// from claimed_at to now. Works for both in-progress and completed tasks.
// GET /api/board/{project}/tasks/{taskID}/cost
func (h *BoardHandler) TaskLiveCost(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	cost, err := h.bs.GetTaskLiveCost(r.Context(), project, taskID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if cost == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"task_id": taskID,
			"message": "cost tracking unavailable (no session_id or proxy not configured)",
		})
		return
	}
	writeJSON(w, http.StatusOK, cost)
}

// RegisterWait parks an agent by registering an active wait condition.
// POST /api/board/{project}/waits
func (h *BoardHandler) RegisterWait(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		SessionName  string `json:"session_name,omitempty"`
		WaitType     string `json:"wait_type"`
		TargetID     string `json:"target_id"`
		Reason       string `json:"reason"`
		Timeout      string `json:"timeout,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	if body.SessionName == "" {
		if sub, err := h.bs.GetProjectSubscription(r.Context(), project, body.SubscriberID); err == nil && sub != nil {
			body.SessionName = sub.SessionName
		}
	}
	timeout := board.DefaultWaitTimeout
	if body.Timeout != "" {
		if d, err := time.ParseDuration(body.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}
	wait, err := h.bs.RegisterWait(r.Context(), project, body.SubscriberID, body.SessionName, body.WaitType, body.TargetID, body.Reason, timeout)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, wait)
}

// GetActiveWait returns active registered wait(s) for a subscriber or board.
// GET /api/board/{project}/waits
func (h *BoardHandler) GetActiveWait(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	subscriberID := r.URL.Query().Get("subscriber_id")
	if subscriberID != "" {
		wait, err := h.bs.GetActiveWait(r.Context(), project, subscriberID)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"wait": wait})
		return
	}
	waits, err := h.bs.ListActiveWaits(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"waits": waits})
}

// CancelWait cancels an active wait for a subscriber.
// DELETE /api/board/{project}/waits
func (h *BoardHandler) CancelWait(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	subscriberID := r.URL.Query().Get("subscriber_id")
	if subscriberID == "" {
		var body struct {
			SubscriberID string `json:"subscriber_id"`
		}
		_ = decodeJSON(r, &body)
		subscriberID = body.SubscriberID
	}
	if subscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	if err := h.bs.CancelActiveWait(r.Context(), project, subscriberID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}

// PollWait long-polls until an active wait is resolved, expired, or cancelled.
// GET /api/board/{project}/waits/poll
func (h *BoardHandler) PollWait(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	subscriberID := r.URL.Query().Get("subscriber_id")
	if subscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	timeoutSec := 30
	if raw := r.URL.Query().Get("timeout"); raw != "" {
		if sec, err := strconv.Atoi(raw); err == nil && sec > 0 {
			if sec > 60 {
				sec = 60
			}
			timeoutSec = sec
		}
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			wait, err := h.bs.GetActiveWait(r.Context(), project, subscriberID)
			if err != nil {
				errInternalServer(w, err.Error())
				return
			}
			if wait == nil {
				writeJSON(w, http.StatusOK, map[string]any{"status": "resolved"})
				return
			}
			if time.Now().After(deadline) {
				writeJSON(w, http.StatusOK, map[string]any{"status": "timeout", "wait": wait})
				return
			}
		}
	}
}

func (h *BoardHandler) resolveMessageWaits(project, senderID, content string) {
	ctx := context.Background()
	resolved, err := h.bs.ResolveMatchingMessageWaits(ctx, project, senderID, content)
	if err != nil || len(resolved) == 0 {
		return
	}
	preview := content
	if len(preview) > 60 {
		preview = preview[:57] + "..."
	}
	for _, w := range resolved {
		sessionName := w.SessionName
		if sub, err := h.bs.GetProjectSubscription(ctx, project, w.SubscriberID); err == nil && sub != nil && sub.SessionName != "" {
			sessionName = sub.SessionName
		}
		if h.terminal != nil && sessionName != "" {
			nudge := fmt.Sprintf("[Wait resolved] %s posted on '%s': %q. Run 'coral-board read' to see the message.", senderID, project, preview)
			if err := h.terminal.SendInput(ctx, sessionName, nudge, "", ""); err != nil {
				slog.Warn("failed to nudge waiting agent", "subscriber", w.SubscriberID, "session", sessionName, "error", err)
			}
		}
	}
}

func (h *BoardHandler) resolveTaskWaits(project string, taskID int64, taskTitle, event string) {
	ctx := context.Background()
	resolved, err := h.bs.ResolveMatchingTaskWaits(ctx, project, taskID)
	if err != nil || len(resolved) == 0 {
		return
	}
	for _, w := range resolved {
		sessionName := w.SessionName
		if sub, err := h.bs.GetProjectSubscription(ctx, project, w.SubscriberID); err == nil && sub != nil && sub.SessionName != "" {
			sessionName = sub.SessionName
		}
		if h.terminal != nil && sessionName != "" {
			nudge := fmt.Sprintf("[Wait resolved] Task #%d (%s) is now %s. Run 'coral-board task detail %d' to review.", taskID, taskTitle, event, taskID)
			if err := h.terminal.SendInput(ctx, sessionName, nudge, "", ""); err != nil {
				slog.Warn("failed to nudge waiting agent", "subscriber", w.SubscriberID, "session", sessionName, "error", err)
			}
		}
	}
}

func (h *BoardHandler) ResolveCommitWaits(ctx context.Context, project, commitHash string) {
	resolved, err := h.bs.ResolveMatchingCommitWaits(ctx, project, commitHash)
	if err != nil || len(resolved) == 0 {
		return
	}
	for _, w := range resolved {
		sessionName := w.SessionName
		if sub, err := h.bs.GetProjectSubscription(ctx, project, w.SubscriberID); err == nil && sub != nil && sub.SessionName != "" {
			sessionName = sub.SessionName
		}
		if h.terminal != nil && sessionName != "" {
			nudge := fmt.Sprintf("[Wait resolved] Commit %s landed on '%s'.", commitHash, project)
			if err := h.terminal.SendInput(ctx, sessionName, nudge, "", ""); err != nil {
				slog.Warn("failed to nudge waiting agent", "subscriber", w.SubscriberID, "session", sessionName, "error", err)
			}
		}
	}
}
