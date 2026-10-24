// Claiming, completing and cancelling a task, and telling what it unblocks (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/tracking"
)

// ClaimTask claims the next available task by priority.
// POST /api/board/{project}/tasks/claim
func (h *BoardHandler) ClaimTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		TaskID       int64  `json:"task_id,omitempty"`
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
	task, err := h.bs.ClaimTask(r.Context(), project, body.SubscriberID, body.TaskID)
	if err != nil {
		if err.Error() == "complete your current task before claiming a new one" {
			errConflict(w, err.Error())
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
	h.goBG(func() {
		notification := fmt.Sprintf("[Task #%d claimed by %s] %s", task.ID, body.SubscriberID, task.Title)
		h.bs.PostSystemMessage(context.Background(), project, "Coral Task Queue", notification, nil)
	})
	writeJSON(w, http.StatusOK, task)
}

// CompleteTaskByID marks a task as completed.
// POST /api/board/{project}/tasks/{taskID}/complete
func (h *BoardHandler) CompleteTaskByID(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		SubscriberID      string               `json:"subscriber_id"`
		Message           *string              `json:"message"`
		Outcome           string               `json:"outcome,omitempty"`
		Artifacts         []board.TaskArtifact `json:"artifacts,omitempty"`
		ExpectedRevision  *int                 `json:"expected_revision,omitempty"`
		CandidateRevision string               `json:"candidate_revision,omitempty"`
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
	unverified := h.vetTaskArtifacts(r, project, body.Artifacts)
	task, err := h.bs.CompleteTaskWithArtifactsAtRevisionAndCandidate(r.Context(), project, taskID, body.SubscriberID, body.Message, body.Outcome, body.Artifacts, body.ExpectedRevision, body.CandidateRevision)
	if err != nil {
		if strings.Contains(err.Error(), "revision") {
			errConflict(w, err.Error())
		} else {
			errTaskStore(w, err)
		}
		return
	}
	h.persistTaskArtifactAfterReply(task)
	// Task outcome and funnel milestones (first completion, first success).
	tracking.TrackTaskCompleted(taskOutcomeForTracking(task, body.Outcome))
	completionMsg := ""
	if body.Message != nil {
		completionMsg = *body.Message
	}
	if task == nil {
		writeJSON(w, http.StatusOK, task)
		return
	}
	h.afterTaskCompleted(project, body.SubscriberID, task, completionMsg, "")
	writeTaskVetted(w, task, unverified)
}

// writeTaskVetted answers with the task, plus artifacts_unverified (how many managed links were
// kept as plain links, and their names) when vetTaskArtifacts marked any: the agent can upload the
// file and complete again.
func writeTaskVetted(w http.ResponseWriter, task any, unverified []string) {
	if len(unverified) == 0 {
		writeJSON(w, http.StatusOK, task)
		return
	}
	raw, err := json.Marshal(task)
	var out map[string]any
	if err != nil || json.Unmarshal(raw, &out) != nil {
		writeJSON(w, http.StatusOK, task)
		return
	}
	out["artifacts_unverified"] = len(unverified)
	out["artifacts_unverified_names"] = unverified
	out["artifacts_unverified_help"] = "these coral://artifacts links are not uploads of this team: they are kept as plain links. Upload the file with coral-agent artifact upload and use its link."
	writeJSON(w, http.StatusOK, out)
}

// afterTaskCompleted posts the completion notice and runs what a completion sets off: the
// orchestrators' notice, the tasks it unblocks, the waits on it, and the completer's next task.
// Shared by a completion and by a review's last approval (#3725), which completes the task as its
// orchestrator. how, when set, says how it was accepted ("approved by ...") and goes in the notice.
func (h *BoardHandler) afterTaskCompleted(project, subscriberID string, completedTask *board.Task, completionMsg, how string) {
	h.goBG(func() {
		ctx := context.Background()
		msg := completedTask.Title
		if completionMsg != "" {
			msg = completionMsg
		}
		by := subscriberID
		if how != "" {
			by += " (" + how + ")"
		}
		notification := fmt.Sprintf("[Task #%d completed by %s] Outcome: %s; %d artifacts. %s", completedTask.ID, by, completedTask.Workflow.Outcome, len(completedTask.Workflow.Artifacts), msg)
		h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", notification, nil)
		h.notifyOrchestratorsTaskCompleted(ctx, project, subscriberID, completedTask, fmt.Sprintf("Outcome: %s. %s", completedTask.Workflow.Outcome, msg))

		// Resolve downstream blocked tasks
		h.notifyUnblockedTasks(ctx, project, completedTask.ID)

		// Resolve active task waits
		h.resolveTaskWaits(project, completedTask.ID, completedTask.Title, "completed")

		// Check if the agent has more pending tasks. Unassigned pool tasks nudge
		// workers, but not the orchestrator; explicitly assigned tasks still do.
		freed := subscriberID
		if completedTask.AssignedTo != nil && *completedTask.AssignedTo != "" {
			freed = *completedTask.AssignedTo
		}
		h.nudgeNextTask(ctx, project, freed)
	})
}

// CancelTaskByID marks a task as skipped/cancelled.
// POST /api/board/{project}/tasks/{taskID}/cancel
func (h *BoardHandler) CancelTaskByID(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
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
	attrWho, attrOK := attributedTo(w, r, body.SubscriberID)
	if !attrOK {
		return
	}
	body.SubscriberID = attrWho
	if body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	// The same rule as complete: cancelling also stalls every task downstream of it.
	if current, err := h.bs.GetTask(r.Context(), project, taskID); err != nil || current == nil {
		errNotFound(w, "Task not found")
		return
	} else if !h.requirePlannerOrAssignee(w, r.Context(), project, current, body.SubscriberID, "cancel it") {
		return
	}
	task, err := h.bs.CancelTask(r.Context(), project, taskID, body.SubscriberID, body.Message)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	h.persistTaskArtifactAfterReply(task)
	stalled, stallErr := h.bs.StallDownstreamTasks(r.Context(), project, task.ID)
	if stallErr != nil {
		slog.Warn("failed to reconcile downstream tasks after cancellation", "project", project, "task_id", task.ID, "error", stallErr)
	}
	h.goBG(func() {
		ctx := context.Background()
		notification := fmt.Sprintf("[Task #%d cancelled by %s] %s", task.ID, body.SubscriberID, task.Title)
		h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", notification, nil)
		// Reconcile the full descendant graph immediately. Pending descendants
		// become blocked/stalled and retain their owner for orchestrator repair.
		if stallErr == nil {
			for _, child := range stalled {
				h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf("@Orchestrator [Task #%d stalled] %s depends on cancelled task #%d; rewire or create a retry before claiming it.", child.ID, child.Title, task.ID), nil)
			}
		}
		h.notifyOrchestratorOfCancelledDependency(ctx, project, body.SubscriberID, task)

		// Only termination dependencies are satisfied by cancellation.
		h.notifyUnblockedTasks(ctx, project, task.ID)

		// Resolve active task waits
		h.resolveTaskWaits(project, task.ID, task.Title, "cancelled")
	})
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
		h.bs.PostSystemMessage(ctx, t.BoardID, "Coral Task Queue", msg, nil)

		// Send terminal nudge to assignee
		if assignee != "" {
			h.sendTaskNudge(ctx, t.BoardID, assignee, taskClaimNudge(t.ID))
		}

		// Resolve active task waits
		h.resolveTaskWaits(t.BoardID, t.ID, t.Title, "unblocked")
	}
}

// taskOutcomeForTracking reduces a completion to the closed success/failed set
// used by analytics, preferring the stored workflow outcome.
func taskOutcomeForTracking(task *board.Task, requested string) string {
	outcome := requested
	if task != nil && task.Workflow.Outcome != "" {
		outcome = task.Workflow.Outcome
	}
	if outcome == "failed" {
		return tracking.OutcomeFailed
	}
	return tracking.OutcomeSuccess
}
