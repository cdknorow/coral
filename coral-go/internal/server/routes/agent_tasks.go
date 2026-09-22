package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/store"
)

// Agent tasks as seen by the agent itself: the API behind `coral-agent task`.
//
// It mirrors the board task API (/api/board/{project}/tasks/...) and its JSON
// shape, keyed by the agent's Coral session instead of a board subscriber:
//
//	GET  /api/agent/tasks?session_id=      {"tasks": [...]}
//	POST /api/agent/tasks                  add {session_id, title, body, priority}
//	POST /api/agent/tasks/claim            next pending -> in_progress (404 when none)
//	POST /api/agent/tasks/current          the task in progress (404 when none)
//	POST /api/agent/tasks/{id}/complete    {session_id, message}
//	POST /api/agent/tasks/{id}/cancel      {session_id, message}
//
// The CLI resolves the session the same way the hooks do; the agent name is
// looked up from the session, so a shell that has cd'ed elsewhere still finds
// its tasks.

func (h *SessionsHandler) sessionAgentName(ctx context.Context, sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	ls, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil || ls == nil || ls.AgentName == "" {
		return "", false
	}
	return ls.AgentName, true
}

// agentTaskView is an agent task in the board task JSON shape.
type agentTaskView struct {
	ID                int64   `json:"id"`
	Title             string  `json:"title"`
	Body              *string `json:"body,omitempty"`
	Status            string  `json:"status"`
	Priority          string  `json:"priority"`
	AssignedTo        *string `json:"assigned_to"`
	CompletionMessage *string `json:"completion_message,omitempty"`
	CreatedAt         string  `json:"created_at"`
	ClaimedAt         *string `json:"claimed_at,omitempty"`
	CompletedAt       *string `json:"completed_at,omitempty"`
	SessionID         *string `json:"session_id,omitempty"`
}

var agentTaskStatus = map[int]string{
	store.AgentTaskPending:    "pending",
	store.AgentTaskDone:       "completed",
	store.AgentTaskInProgress: "in_progress",
	store.AgentTaskCancelled:  "skipped",
}

func viewAgentTask(t *store.AgentTask) agentTaskView {
	priority := "medium"
	if t.Priority != nil && *t.Priority != "" {
		priority = *t.Priority
	}
	assignee := t.AgentName
	if t.DisplayName != nil && *t.DisplayName != "" {
		assignee = *t.DisplayName
	}
	status := agentTaskStatus[t.Completed]
	if status == "" {
		status = "pending"
	}
	return agentTaskView{
		ID: t.ID, Title: t.Title, Body: t.Body, Status: status, Priority: priority,
		AssignedTo: &assignee, CompletionMessage: t.CompletionMessage, CreatedAt: t.CreatedAt,
		ClaimedAt: t.StartedAt, CompletedAt: t.CompletedAt, SessionID: t.SessionID,
	}
}

// agentFromRequest resolves the calling agent from a session ID, writing the
// error response when it is unknown.
func (h *SessionsHandler) agentFromRequest(w http.ResponseWriter, r *http.Request, sessionID string) (string, bool) {
	name, ok := h.sessionAgentName(r.Context(), sessionID)
	if !ok {
		errNotFound(w, "Unknown session; run this from inside a Coral agent")
	}
	return name, ok
}

type agentTaskRequest struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Priority  string `json:"priority"`
	Message   string `json:"message"`
}

func decodeAgentTaskRequest(w http.ResponseWriter, r *http.Request) (agentTaskRequest, bool) {
	var body agentTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return body, false
	}
	return body, true
}

// ListAgentTasksForAgent lists the calling agent's tasks.
// GET /api/agent/tasks?session_id=...
func (h *SessionsHandler) ListAgentTasksForAgent(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	name, ok := h.agentFromRequest(w, r, sid)
	if !ok {
		return
	}
	tasks, err := h.ts.ListAgentTasks(r.Context(), name, &sid)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	views := make([]agentTaskView, 0, len(tasks))
	for i := range tasks {
		views = append(views, viewAgentTask(&tasks[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": views})
}

// AddAgentTaskForAgent lets the agent add a task to its own list.
// POST /api/agent/tasks {"session_id", "title", "body", "priority"}
func (h *SessionsHandler) AddAgentTaskForAgent(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeAgentTaskRequest(w, r)
	if !ok {
		return
	}
	body.Title = oneLineTitle(body.Title)
	if body.Title == "" {
		errBadRequest(w, "title is required")
		return
	}
	name, ok := h.agentFromRequest(w, r, body.SessionID)
	if !ok {
		return
	}
	task, err := h.ts.CreateAgentTask(r.Context(), name, body.Title, &body.SessionID, nil)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	h.ts.SetAgentTaskDetails(r.Context(), task.ID, body.Body, body.Priority)
	task, _ = h.ts.GetAgentTask(r.Context(), task.ID)
	writeJSON(w, http.StatusCreated, viewAgentTask(task))
}

// ClaimAgentTaskForAgent marks the calling agent's next pending task in
// progress and returns it (404 when none is pending, as on a board).
// POST /api/agent/tasks/claim {"session_id"}
func (h *SessionsHandler) ClaimAgentTaskForAgent(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeAgentTaskRequest(w, r)
	if !ok {
		return
	}
	name, ok := h.agentFromRequest(w, r, body.SessionID)
	if !ok {
		return
	}
	task, err := h.ts.ClaimNextAgentTask(r.Context(), name, &body.SessionID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if task == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "No available tasks"})
		return
	}
	writeJSON(w, http.StatusOK, viewAgentTask(task))
}

// CurrentAgentTaskForAgent returns the calling agent's task in progress.
// POST /api/agent/tasks/current {"session_id"}
func (h *SessionsHandler) CurrentAgentTaskForAgent(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeAgentTaskRequest(w, r)
	if !ok {
		return
	}
	name, ok := h.agentFromRequest(w, r, body.SessionID)
	if !ok {
		return
	}
	task, err := h.ts.CurrentAgentTask(r.Context(), name, &body.SessionID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if task == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "No active task"})
		return
	}
	writeJSON(w, http.StatusOK, viewAgentTask(task))
}

// CompleteAgentTaskForAgent marks one of the calling agent's tasks done.
// POST /api/agent/tasks/{taskID}/complete {"session_id", "message"}
func (h *SessionsHandler) CompleteAgentTaskForAgent(w http.ResponseWriter, r *http.Request) {
	h.finishAgentTask(w, r, store.AgentTaskDone)
}

// CancelAgentTaskForAgent cancels one of the calling agent's tasks.
// POST /api/agent/tasks/{taskID}/cancel {"session_id", "message"}
func (h *SessionsHandler) CancelAgentTaskForAgent(w http.ResponseWriter, r *http.Request) {
	h.finishAgentTask(w, r, store.AgentTaskCancelled)
}

func (h *SessionsHandler) finishAgentTask(w http.ResponseWriter, r *http.Request, state int) {
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task id")
		return
	}
	body, ok := decodeAgentTaskRequest(w, r)
	if !ok {
		return
	}
	name, ok := h.agentFromRequest(w, r, body.SessionID)
	if !ok {
		return
	}
	task, err := h.ts.GetAgentTask(r.Context(), taskID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !taskBelongsTo(task, name, body.SessionID) {
		errNotFound(w, "No such task for this agent")
		return
	}
	if err := h.ts.FinishAgentTask(r.Context(), taskID, state, body.Message); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	task, _ = h.ts.GetAgentTask(r.Context(), taskID)
	writeJSON(w, http.StatusOK, viewAgentTask(task))
}

func taskBelongsTo(t *store.AgentTask, agentName, sessionID string) bool {
	if t == nil || t.AgentName != agentName {
		return false
	}
	return t.SessionID == nil || *t.SessionID == sessionID
}

// createdTaskResponse is a created agent task plus, when the agent was told
// to claim it, the prompt that was sent (or why sending failed).
type createdTaskResponse struct {
	*store.AgentTask
	Notified    string `json:"notified,omitempty"`
	NotifyError string `json:"notify_error,omitempty"`
}

// claimPrompt is what an agent is told when the operator gives it a task:
// short, naming the task and the commands to claim and finish it. The
// details come with the claim.
// oneLineTitle collapses a task title's whitespace (including newlines).
func oneLineTitle(title string) string {
	return strings.Join(strings.Fields(title), " ")
}

func claimPrompt(t *store.AgentTask) string {
	title := oneLineTitle(t.Title)
	return fmt.Sprintf("You have a new task in Coral (#%d: %s). Claim it with `coral-agent task claim` to see the details, then run `coral-agent task complete %d` when it's done.", t.ID, title, t.ID)
}

// notifyTaskCreated types the claim prompt into the agent's terminal.
func (h *SessionsHandler) notifyTaskCreated(ctx context.Context, name, sessionID string, t *store.AgentTask) (string, string) {
	agentType := ""
	if sessionID != "" {
		if ls, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && ls != nil {
			agentType = ls.AgentType
		}
	}
	prompt := claimPrompt(t)
	if err := h.terminal.SendInput(ctx, name, prompt, agentType, sessionID); err != nil {
		return "", err.Error()
	}
	return prompt, ""
}
