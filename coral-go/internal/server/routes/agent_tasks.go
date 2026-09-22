package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/store"
)

// Agent tasks as seen by the agent itself.
//
// An agent that is not on a team board gets tasks from the operator on its
// own task list in Coral. The operator's prompt tells it to claim them with
// `coral-board task claim`, which (outside a board) calls these endpoints.
// They are keyed by the agent's Coral session ID, which the CLI resolves the
// same way the hooks do; the agent name is looked up from the session, so a
// shell that has cd'ed elsewhere still finds its tasks.

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

// ListSessionAgentTasks lists the calling agent's tasks.
// GET /api/agent-tasks?session_id=...
func (h *SessionsHandler) ListSessionAgentTasks(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	name, ok := h.sessionAgentName(r.Context(), sid)
	if !ok {
		errNotFound(w, "Unknown session; run this from inside a Coral agent")
		return
	}
	tasks, err := h.ts.ListAgentTasks(r.Context(), name, &sid)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tasks))
}

// ClaimSessionAgentTask marks the calling agent's next pending task in
// progress and returns it.
// POST /api/agent-tasks/claim {"session_id": "..."}
func (h *SessionsHandler) ClaimSessionAgentTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	name, ok := h.sessionAgentName(r.Context(), body.SessionID)
	if !ok {
		errNotFound(w, "Unknown session; run this from inside a Coral agent")
		return
	}
	task, err := h.ts.ClaimNextAgentTask(r.Context(), name, &body.SessionID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if task == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "No pending tasks"})
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// CompleteSessionAgentTask marks one of the calling agent's tasks done.
// POST /api/agent-tasks/{taskID}/complete {"session_id": "..."}
func (h *SessionsHandler) CompleteSessionAgentTask(w http.ResponseWriter, r *http.Request) {
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task id")
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	name, ok := h.sessionAgentName(r.Context(), body.SessionID)
	if !ok {
		errNotFound(w, "Unknown session; run this from inside a Coral agent")
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
	done := 1
	if err := h.ts.UpdateAgentTask(r.Context(), taskID, nil, &done, nil); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	task, _ = h.ts.GetAgentTask(r.Context(), taskID)
	writeJSON(w, http.StatusOK, task)
}

func taskBelongsTo(t *store.AgentTask, agentName, sessionID string) bool {
	if t == nil || t.AgentName != agentName {
		return false
	}
	return t.SessionID == nil || *t.SessionID == sessionID
}
