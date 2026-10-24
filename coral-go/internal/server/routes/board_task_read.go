// Listing and reading tasks, their live cost and their changes (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
)

// ListTasks returns a page of the board's task list (#3269).
// GET /api/board/{project}/tasks?status=open|completed|all&limit=50&cursor=...
//
// Items carry list fields only (board.TaskListItem); the full task is at
// GET /api/board/{project}/tasks/{taskID}. With no status: every open task plus the
// newest done tasks, and next_cursor continues with status=completed. full=1 returns
// every task in full, as before pagination, for callers that need it.
func (h *BoardHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	h.setBoardIdentity(w, r, project)
	q := r.URL.Query()
	if q.Get("full") == "1" {
		// Every task in full, but not without end: a viewer or an agent can ask for it (#4602). Past maxFullTaskRows the reply
		// is cut and says so; the paged list (no full=1) reaches the rest.
		tasks, more, err := h.bs.ListTasksCapped(r.Context(), project, maxFullTaskRows)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		reply := map[string]any{"tasks": emptyIfNil(tasks)}
		if more {
			reply["truncated"] = true
		}
		writeJSON(w, http.StatusOK, reply)
		return
	}
	query := board.TaskListQuery{Status: q.Get("status"), Cursor: q.Get("cursor")}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			errBadRequest(w, "limit must be a positive number")
			return
		}
		query.Limit = n
	}
	page, err := h.bs.ListTaskPage(r.Context(), project, query)
	if errors.Is(err, board.ErrBadTaskListQuery) {
		errBadRequest(w, err.Error())
		return
	}
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// Polled every 10 s per open board: an unchanged page is a 304 with no body (#3439).
	httputil.WriteJSONValidated(w, r, http.StatusOK, page)
}

// ListAllTasks returns the most recent tasks across all boards.
// GET /api/board/tasks?limit=100
//
// With no parameters (or only limit) the reply is the whole rows of the newest tasks, as it has always been. Any other
// parameter is the task query (queryAllTasks, board_task_query.go, #4540): filters, sort, cursor paging and a choice of fields.
func (h *BoardHandler) ListAllTasks(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" && h.queryAllTasks(w, r) {
		return
	}
	// At most 500 whole rows (#4602; the task query path caps at 500 too, and 100 with long text).
	limit := clampLimit(queryInt(r, "limit", 100), 100, maxAllTasksRows)
	tasks, err := h.bs.ListAllTasks(r.Context(), limit)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	ids := make([]string, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].BoardID
	}
	labels := h.bs.BoardLabels(r.Context(), ids)
	for i := range tasks {
		tasks[i].BoardLabel = labels[tasks[i].BoardID]
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
	task, err := h.bs.GetTask(r.Context(), urlParam(r, "project"), id)
	if err != nil {
		errNotFound(w, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// POST /api/board/{project}/tasks/current
func (h *BoardHandler) ActiveTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	attrWho, attrOK := attributedTo(w, r, body.SubscriberID)
	if !attrOK {
		return
	}
	body.SubscriberID = attrWho
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

// TaskChangesDiff serves the patch captured when a task completed or was skipped.
// GET /api/board/{project}/tasks/{taskID}/changes.diff
func (h *BoardHandler) TaskChangesDiff(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
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
	var found *board.Task
	for i := range tasks {
		if tasks[i].ID == taskID {
			found = &tasks[i]
			break
		}
	}
	if found == nil {
		errNotFound(w, "task not found")
		return
	}
	if h.coralDir == "" {
		errNotFound(w, "task changes artifact not found")
		return
	}
	// The diff is kept by board and id; one written before that (by id alone) is still read, so no task loses the history it had. Either must have been written after the task was made: a file older than the task is an
	// older task's under a repeated id (#6948).
	var data []byte
	served := false
	for _, path := range []string{h.taskArtifactPath(found.BoardID, taskID), h.legacyTaskArtifactPath(taskID)} {
		if _, serr := os.Stat(path); serr != nil {
			continue
		}
		// Judged by the task's own folder, not the file: identical diffs are stored once and every task's changes.diff is a hard link to that object (#3555), so a task whose diff repeats an older one's has a file with the
		// older time. Putting this task's link in its folder is what sets the folder's time, and a folder is never shared.
		if dir, derr := os.Stat(filepath.Dir(path)); derr != nil || writtenBefore(dir.ModTime(), found.CreatedAt) {
			continue
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			errInternalServer(w, rerr.Error())
			return
		}
		data, served = b, true
		break
	}
	if !served {
		errNotFound(w, "task changes artifact not found")
		return
	}
	if cloud.Tunneled(r) {
		data = safeDiff(data) // through Coral Hub: filtered and capped, old diffs too (#3555)
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="changes.diff"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// TaskLiveCost returns real-time cost for a task by querying proxy_requests
// from claimed_at to now. Works for both in-progress and completed tasks.
// GET /api/board/{project}/tasks/{taskID}/cost
func (h *BoardHandler) TaskLiveCost(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	cost, err := h.bs.GetTaskLiveCost(r.Context(), project, taskID)
	if errors.Is(err, board.ErrTaskNotFound) {
		errNotFound(w, "task not found") // a missing board or task is a 404, not a server error (#3561)
		return
	}
	if err != nil {
		errInternalServer(w, "could not read the task's cost")
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
