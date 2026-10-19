// Editing, amending, reassigning and publishing a task (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/cdknorow/coral/internal/board"
	"github.com/go-chi/chi/v5"
)

// UpdateTask applies partial edits to a pending, in_progress, or blocked task.
// PATCH /api/board/{project}/tasks/{taskID}
func (h *BoardHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var edit taskEdit
	if err := decodeJSON(r, &edit); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	attrWho, attrOK := attributedTo(w, r, edit.SubscriberID)
	if !attrOK {
		return
	}
	edit.SubscriberID = attrWho
	if !h.allowTaskEdit(w, r, project, taskID, &edit) {
		return
	}
	before, task, prevStatus, err := h.applyTaskEdit(r.Context(), project, taskID, &edit)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	_ = h.bs.TouchTask(r.Context(), project, taskID)
	reassigned := edit.AssignedTo != nil
	h.goBG(func() { h.announceTaskEdit(context.Background(), project, reassigned, before, task, prevStatus) })
	writeJSON(w, http.StatusOK, task)
}

// taskEdit is an UpdateTask request: edits to a task's title, priority, assignee or blockers.
type taskEdit struct {
	SubscriberID  string          `json:"subscriber_id"`
	Title         *string         `json:"title,omitempty"`
	Body          *string         `json:"body,omitempty"`
	Priority      *string         `json:"priority,omitempty"`
	AssignedTo    *string         `json:"assigned_to,omitempty"`
	BlockedBy     json.RawMessage `json:"blocked_by,omitempty"`
	RemoveBlocker *int64          `json:"remove_blocker,omitempty"`
	ClearBlockers bool            `json:"clear_blockers,omitempty"`
}

func (e *taskEdit) removesBlockers() bool { return e.RemoveBlocker != nil || e.ClearBlockers }

// allowTaskEdit refuses an edit that is too large, changes the body (that is /amend), mixes removing blockers with
// other edits, or isn't its actor's to make: assigning or removing blockers is a planner's, the title or priority
// a planner's or the assignee's, and an archived member is never assigned.
func (h *BoardHandler) allowTaskEdit(w http.ResponseWriter, r *http.Request, project string, taskID int64, e *taskEdit) bool {
	if e.Title != nil && refuseTextTooLarge(w, "title_too_large", "a task title", len(*e.Title), maxTaskTitleBytes) {
		return false
	}
	if e.Body != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "task body changes require the revisioned /amend endpoint with a reason and base_revision"})
		return false
	}
	removing := e.removesBlockers()
	if removing && (len(e.BlockedBy) > 0 || e.Title != nil || e.Priority != nil || e.AssignedTo != nil || (e.RemoveBlocker != nil && (e.ClearBlockers || *e.RemoveBlocker <= 0))) {
		errBadRequest(w, "use either remove_blocker (positive task ID) or clear_blockers, without other edits")
		return false
	}
	if (e.AssignedTo != nil || removing) && !h.requireTaskPlanner(w, r, project, e.SubscriberID) {
		return false
	}
	// A task's title is typed into its assignee's terminal: only a planner or the assignee
	// changes what it says (#3668).
	if e.Title != nil || e.Priority != nil {
		current, err := h.bs.GetTask(r.Context(), project, taskID)
		if err != nil || current == nil {
			errNotFound(w, "Task not found")
			return false
		}
		if !h.requirePlannerOrAssignee(w, r.Context(), project, current, e.SubscriberID, "change its title or priority") {
			return false
		}
	}
	return e.AssignedTo == nil || !h.refuseArchivedAssignee(w, r.Context(), project, *e.AssignedTo)
}

// applyTaskEdit makes the edit: its blockers removed, or its fields and blocked_by updated. It returns the task
// before and after, and the status it had.
func (h *BoardHandler) applyTaskEdit(ctx context.Context, project string, taskID int64, e *taskEdit) (before, task *board.Task, prevStatus string, err error) {
	update := board.TaskUpdate{
		Title:      e.Title,
		Body:       e.Body,
		Priority:   e.Priority,
		AssignedTo: e.AssignedTo,
	}
	if len(e.BlockedBy) > 0 {
		deps, err := parseBlockedBy(e.BlockedBy, project)
		if err != nil {
			return nil, nil, "", err
		}
		update.BlockedBy = &deps
	}
	before, _ = h.bs.GetTask(ctx, project, taskID)
	if e.removesBlockers() {
		var blocker int64
		if e.RemoveBlocker != nil {
			blocker = *e.RemoveBlocker
		}
		task, prevStatus, err = h.bs.RemoveTaskBlockers(ctx, project, taskID, blocker)
	} else {
		task, prevStatus, err = h.bs.UpdateTask(ctx, project, taskID, update, 32)
	}
	return before, task, prevStatus, err
}

// announceTaskEdit posts that the task was edited and, when its blockers changed its status, that it is blocked
// or ready (nudging a ready task's assignee); a pending task given to someone new nudges them to claim it, unless
// that nudge was just sent or they are busy with another task.
func (h *BoardHandler) announceTaskEdit(ctx context.Context, project string, reassigned bool, before, task *board.Task, prevStatus string) {
	h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf("[Task #%d edited] %s", task.ID, task.Title), nil)
	assignee := taskAssignee(task)
	nudged := false
	switch {
	case prevStatus == "pending" && task.Status == "blocked":
		if assignee != "" {
			msg := fmt.Sprintf("[Task #%d blocked] %s — @%s task is now blocked, please pause work", task.ID, task.Title, assignee)
			h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", msg, nil)
		}
	case prevStatus == "blocked" && task.Status == "pending":
		msg := fmt.Sprintf("[Task #%d unblocked] %s", task.ID, task.Title)
		if assignee != "" {
			msg += fmt.Sprintf(" — @%s your task is now ready", assignee)
		}
		h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", msg, nil)
		if assignee != "" {
			h.sendTaskNudge(ctx, project, assignee, taskClaimNudge(task.ID))
			nudged = true
		}
	}
	if !reassigned || task.Status != "pending" || nudged || assignee == "" || assignee == taskAssignee(before) {
		return
	}
	if hasActive, _ := h.bs.HasActiveTaskForAssignee(ctx, project, assignee, task.ID); !hasActive {
		h.sendTaskNudge(ctx, project, assignee, taskClaimNudge(task.ID))
	}
}

// taskAssignee is who a task is assigned to, or "" (also for no task).
func taskAssignee(t *board.Task) string {
	if t == nil || t.AssignedTo == nil {
		return ""
	}
	return *t.AssignedTo
}

// AmendTask applies a planner-authorized, revision-checked body/instruction amendment.
// PATCH /api/board/{project}/tasks/{taskID}/amend
func (h *BoardHandler) AmendTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		SubscriberID string                 `json:"subscriber_id"`
		BaseRevision int                    `json:"base_revision"`
		Reason       string                 `json:"reason"`
		Changes      map[string]interface{} `json:"changes"`
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
	if text, ok := body.Changes["body"].(string); ok && refuseTextTooLarge(w, "body_too_large", "a task body", len(text), maxTaskBodyBytes) {
		return
	}
	if !h.requireTaskPlanner(w, r, project, body.SubscriberID) {
		return
	}
	task, err := h.bs.AmendTask(r.Context(), project, taskID, body.SubscriberID, body.BaseRevision, body.Reason, body.Changes)
	if err != nil {
		if strings.Contains(err.Error(), "revision") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		} else {
			errBadRequest(w, err.Error())
		}
		return
	}
	if err := h.bs.RecordAmendIdentity(r.Context(), project, task.ID, task.Revision, h.claimIdentity(r, project, body.SubscriberID)); err != nil {
		slog.Warn("record amendment identity failed", "project", project, "task", task.ID, "error", err)
	}
	h.goBG(func() {
		notice := fmt.Sprintf("[Task #%d amended to revision %d] %s", task.ID, task.Revision, task.Title)
		if task.AssignedTo != nil && *task.AssignedTo != "" {
			notice += fmt.Sprintf(" — @%s reread task detail before acting", *task.AssignedTo)
		}
		h.bs.PostSystemMessage(context.Background(), project, "Coral Task Queue", notice, nil)
	})
	writeJSON(w, http.StatusOK, task)
}

// ReassignTask changes ownership, preserving any prerequisite block.
// POST /api/board/{project}/tasks/{taskID}/reassign
func (h *BoardHandler) ReassignTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
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
	attrWho, attrOK := attributedTo(w, r, body.SubscriberID)
	if !attrOK {
		return
	}
	body.SubscriberID = attrWho
	if !h.requireTaskPlanner(w, r, project, body.SubscriberID) {
		return
	}
	if h.refuseArchivedAssignee(w, r.Context(), project, body.Assignee) {
		return
	}
	task, err := h.bs.ReassignTask(r.Context(), project, taskID, body.Assignee)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	h.goBG(func() {
		ctx := context.Background()
		notification := h.buildAssignmentNotification(ctx, project, task, body.Assignee, true)
		h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", notification, nil)
		if body.Assignee != "" && task.Status == "pending" {
			if hasActive, _ := h.bs.HasActiveTaskForAssignee(ctx, project, body.Assignee, task.ID); !hasActive {
				h.sendTaskNudge(ctx, project, body.Assignee, taskClaimNudge(task.ID))
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
			h.bs.PostSystemMessage(ctx, t.BoardID, "Coral Task Queue", msg, nil)
		}
	})
	writeJSON(w, http.StatusOK, task)
}

// PublishTask transitions a draft task to pending or blocked.
// POST /api/board/{project}/tasks/{taskID}/publish
func (h *BoardHandler) PublishTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	// Publishing releases planned work to the workers: a planner's step (#3668).
	var body struct {
		SubscriberID string `json:"subscriber_id"`
	}
	if r.ContentLength != 0 {
		_ = decodeJSON(r, &body)
	}
	actor, attrOK := attributedTo(w, r, taskActor(r, body.SubscriberID))
	if !attrOK {
		return
	}
	if actor == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	if !h.requireTaskPlanner(w, r, project, actor) {
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
		h.goBG(func() {
			deps, _ := h.bs.GetTaskDependencies(context.Background(), task.ID)
			blockerList := formatBlockerList(deps)
			notification := fmt.Sprintf("[Task #%d published (blocked)] %s — blocked by %s", task.ID, task.Title, blockerList)
			h.bs.PostSystemMessage(context.Background(), project, "Coral Task Queue", notification, nil)
		})
	} else {
		h.goBG(func() {
			ctx := context.Background()
			notification := fmt.Sprintf("[Task #%d published] %s", task.ID, task.Title)
			if assignee != "" {
				notification += fmt.Sprintf(" — @%s", assignee)
			}
			h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", notification, nil)

			if assignee != "" {
				hasActive, _ := h.bs.HasActiveTaskForAssignee(ctx, project, assignee, task.ID)
				if !hasActive {
					h.sendTaskNudge(ctx, project, assignee, taskClaimNudge(task.ID))
				}
			}
		})
	}
	writeJSON(w, http.StatusOK, task)
}
