// Who may plan tasks, and creating a task (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/board"
)

// requireTaskPlanner follows the local board identity model: subscriber_id
// identifies the caller, while privileges come from the active registration
// on this board. Personal task endpoints use a separate store/scope.
func (h *BoardHandler) requireTaskPlanner(w http.ResponseWriter, r *http.Request, project, actor string) bool {
	// Operator is the reserved identity used by the desktop dashboard. Server
	// auth already gates remote requests; local clients share the desktop trust
	// boundary. This is not cryptographic attestation of a human caller.
	// The Operator is the actor the identity layer verified (#4255), in any spelling the caller used;
	// the exact name "Operator" in the claim is the earlier test: kept in log mode, where a claimant with
	// nothing behind the name passes and is counted, and gone under enforce.
	if callerActor(r).IsOperator() {
		return true
	}
	if actor == "Operator" {
		if identityActorEnforced() {
			errForbidden(w, "only the Operator or an active registered orchestrator may create or reassign shared tasks")
			return false
		}
		noteEnforceDiffers("task-planner", r)
		return true
	}
	if err := h.bs.RequireTaskReviewer(r.Context(), project, actor); err != nil {
		errForbidden(w, "only Operator or an active registered orchestrator may create or reassign shared tasks; coordinate with your orchestrator")
		return false
	}
	return true
}

// isTaskPlanner reports whether actor plans this board's tasks: the Operator or an active
// registered orchestrator.
func (h *BoardHandler) isTaskPlanner(ctx context.Context, project, actor string) bool {
	return actor == "Operator" || (actor != "" && h.bs.RequireTaskReviewer(ctx, project, actor) == nil)
}

// requirePlannerOrAssignee answers 400 without an actor, 403 unless actor plans tasks or is
// the task's assignee (#3668, the task handlers' follow-up: cancel, title and priority edits,
// nudges and reminders). It reports whether the request may go on.
func (h *BoardHandler) requirePlannerOrAssignee(w http.ResponseWriter, ctx context.Context, project string, task *board.Task, actor, what string) bool {
	if actor == "" {
		errBadRequest(w, "subscriber_id required")
		return false
	}
	if h.isTaskPlanner(ctx, project, actor) || (task.AssignedTo != nil && *task.AssignedTo == actor) {
		return true
	}
	errForbidden(w, "only the task's assignee, Operator or an active registered orchestrator may "+what)
	return false
}

// taskActor is who acts on a task: the JSON body's subscriber_id if the handler read one, else
// the query's (a DELETE, or the dashboard's nudge, carries no body). The identity layer binds
// both to the caller's token.
func taskActor(r *http.Request, fromBody string) string {
	if a := strings.TrimSpace(fromBody); a != "" {
		return a
	}
	return strings.TrimSpace(r.URL.Query().Get("subscriber_id"))
}

// CreateTask creates a new task on a board.
// POST /api/board/{project}/tasks
func (h *BoardHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
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
		RemindAt     string             `json:"remind_at,omitempty"` // RFC 3339: a one-time reminder (#4175 step 3)
		GoalID       *int64             `json:"goal_id,omitempty"`   // a goal of this board the task works toward (#7087)
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Title == "" {
		errBadRequest(w, "title required")
		return
	}
	if refuseTextTooLarge(w, "title_too_large", "a task title", len(body.Title), maxTaskTitleBytes) ||
		refuseTextTooLarge(w, "body_too_large", "a task body", len(body.Body), maxTaskBodyBytes) {
		return
	}
	var remindAt time.Time
	if body.RemindAt != "" {
		at, err := time.Parse(time.RFC3339, body.RemindAt)
		if now := time.Now(); err != nil || !at.After(now) || at.After(now.AddDate(0, 0, maxParkDays)) {
			errBadRequest(w, fmt.Sprintf("remind_at: an RFC 3339 time in the next %d days", maxParkDays))
			return
		}
		remindAt = at
	}
	if refuseNewBoardName(w, r, h.bs, project) {
		return
	}
	attrClaim := body.SubscriberID
	if attrClaim == "" {
		attrClaim = body.CreatedBy
	}
	attrWho, attrOK := attributedTo(w, r, attrClaim)
	if !attrOK {
		return
	}
	if identityActorEnforced() { // the attributed name is the creator, whichever alias was used
		body.SubscriberID, body.CreatedBy = attrWho, attrWho
	}
	createdBy := body.CreatedBy
	if createdBy == "" {
		createdBy = body.SubscriberID
	}
	if createdBy == "" {
		errBadRequest(w, "created_by or subscriber_id required")
		return
	}
	// subscriber_id is the request actor; created_by remains a legacy alias.
	// Never let an attribution field override an explicitly supplied actor.
	if body.SubscriberID != "" && createdBy != body.SubscriberID {
		errForbidden(w, "created_by must match subscriber_id")
		return
	}
	if !h.requireTaskPlanner(w, r, project, createdBy) {
		return
	}
	// An agent token may have at most maxOpenTasksPerAgent open tasks it created on a board (#4602, M10); the Operator and
	// Coral's own tasks are not counted.
	if callerActor(r).Kind == ActorToken {
		open, err := h.bs.CountOpenTasksCreatedBy(r.Context(), project, createdBy)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if open >= maxOpenTasksPerAgent {
			msg := fmt.Sprintf("an agent can have at most %d open tasks it created on a board (it has %d): complete or cancel some first", maxOpenTasksPerAgent, open)
			writeJSON(w, http.StatusConflict, map[string]any{"error": msg, "message": msg, "code": "too_many_open_tasks", "max": maxOpenTasksPerAgent, "not_saved": true})
			return
		}
	}
	if body.Priority == "" {
		body.Priority = "medium"
	}

	// How the creator's name is backed: the server's to say, whatever the request sent (#4175).
	body.Workflow.CreatedIdentity = h.claimIdentity(r, project, createdBy)
	body.Workflow.AmendIdentity = nil
	opts := &board.CreateTaskOpts{Draft: body.Draft, Workflow: body.Workflow, GoalID: body.GoalID}
	if len(body.BlockedBy) > 0 {
		deps, err := parseBlockedBy(body.BlockedBy, project)
		if err != nil {
			errBadRequest(w, err.Error())
			return
		}
		opts.BlockedBy, opts.MaxDepth = deps, 32
	}

	if h.refuseArchivedAssignee(w, r.Context(), project, body.AssignedTo) {
		return
	}
	task, err := h.bs.CreateTaskWithOpts(r.Context(), project, body.Title, body.Body, body.Priority, createdBy, opts, body.AssignedTo)
	if err != nil {
		if !goalError(w, err) {
			errBadRequest(w, err.Error())
		}
		return
	}
	if !remindAt.IsZero() {
		if err := h.bs.SetTaskRemindAt(r.Context(), project, task.ID, remindAt); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		at := remindAt.UTC().Format(time.RFC3339)
		task.RemindAt = &at
	}

	assignee := ""
	if task.AssignedTo != nil {
		assignee = *task.AssignedTo
	}
	if task.Status == "draft" {
		h.goBG(func() {
			notification := fmt.Sprintf("[Task #%d (draft)] %s", task.ID, task.Title)
			h.bs.PostSystemMessage(context.Background(), project, "Coral Task Queue", notification, nil)
		})
	} else if task.Status == "blocked" {
		h.goBG(func() {
			deps, _ := h.bs.GetTaskDependencies(context.Background(), task.ID)
			blockerList := formatBlockerList(deps)
			notification := fmt.Sprintf("[Task #%d (blocked)] %s — blocked by %s", task.ID, task.Title, blockerList)
			h.bs.PostSystemMessage(context.Background(), project, "Coral Task Queue", notification, nil)
		})
	} else {
		h.goBG(func() {
			ctx := context.Background()
			notification := h.buildAssignmentNotification(ctx, project, task, assignee, false)
			h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", notification, nil)

			// Send direct terminal nudge
			if h.terminal != nil {
				if assignee != "" {
					hasActive, _ := h.bs.HasActiveTaskForAssignee(ctx, project, assignee, task.ID)
					if !hasActive {
						h.sendTaskNudge(ctx, project, assignee, taskClaimNudge(task.ID))
					}
				} else {
					idle := h.bs.FindIdleSubscriber(ctx, project, func(sub *board.Subscriber) bool {
						return h.isArchivedSession != nil && h.isArchivedSession(ctx, sub.SessionName)
					})
					if idle != nil && idle.SessionName != "" {
						if err := h.sendAutomated(ctx, project, idle.SessionName, taskNudge); err != nil {
							slog.Warn("failed to nudge agent", "subscriber", idle.SubscriberID, "session", idle.SessionName, "error", err)
						}
					}
				}
			}
		})
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

// The sizes of the text a task and a wait may carry (#4602, M10): a task's title is typed into its assignee's terminal and
// its body is read by every agent that opens it, so neither is allowed to be a document. A larger one is a 413 that says
// so; nothing is stored.
const (
	maxTaskTitleBytes  = 512
	maxTaskBodyBytes   = 256 << 10
	maxWaitReasonBytes = 4 << 10
	// maxOpenTasksPerAgent is how many open tasks one agent token may have created on a board.
	maxOpenTasksPerAgent = 500
)

// refuseTextTooLarge answers 413 with code, and reports true, when got is over max bytes. what names the text ("a task title").
func refuseTextTooLarge(w http.ResponseWriter, code, what string, got, max int) bool {
	if got <= max {
		return false
	}
	msg := fmt.Sprintf("%s is at most %d bytes (this one is %d): shorten it; long text belongs in an artifact (coral-agent artifact upload) with its link in the text", what, max, got)
	writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": msg, "message": msg, "code": code, "max_bytes": max, "not_saved": true})
	return true
}

// maxFullTaskRows caps GET /api/board/{project}/tasks?full=1, and maxAllTasksRows the plain GET /api/board/tasks (#4602).
const (
	// maxBoardPostContent is the longest board post (#4602).
	maxBoardPostContent = 256 << 10
	maxFullTaskRows     = 500
	maxAllTasksRows     = 500
)
