package routes

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cdknorow/coral/internal/board"
	"github.com/go-chi/chi/v5"
)

// SubmitCompletionReview preserves a candidate; it does not complete the task.
func (h *BoardHandler) SubmitCompletionReview(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	id, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		SubscriberID      string               `json:"subscriber_id"`
		Message           string               `json:"message"`
		Outcome           string               `json:"outcome"`
		Reason            string               `json:"reason"`
		Artifacts         []board.TaskArtifact `json:"artifacts"`
		ExpectedRevision  *int                 `json:"expected_revision,omitempty"`
		CandidateRevision string               `json:"candidate_revision,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil || body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id and valid JSON required")
		return
	}
	task, err := h.bs.SubmitCompletionReviewAtRevisionAndCandidate(r.Context(), project, id, body.SubscriberID, body.Message, body.Outcome, body.Reason, body.Artifacts, body.ExpectedRevision, body.CandidateRevision)
	if err != nil {
		if strings.Contains(err.Error(), "revision") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		} else {
			errBadRequest(w, err.Error())
		}
		return
	}
	h.bs.PostMessage(r.Context(), project, "Coral Task Queue", fmt.Sprintf("[Task #%d completion review requested] Candidate saved; completion is not accepted. An orchestrator may release the worker slot with task release-review. Reason: %s", id, body.Reason), nil)
	writeJSON(w, http.StatusOK, task)
}

func (h *BoardHandler) ReleaseCompletionReview(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	id, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		Reason       string `json:"reason"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if err := h.bs.RequireTaskReviewer(r.Context(), project, body.SubscriberID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	task, err := h.bs.ReleaseCompletionReview(r.Context(), project, id, body.SubscriberID, body.Reason)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	h.stopReminder(project, id)
	h.bs.PostMessage(r.Context(), project, "Coral Task Queue", fmt.Sprintf("[Task #%d pending completion review] %s released the worker slot. No completion outcome was accepted. Reason: %s", id, body.SubscriberID, body.Reason), nil)
	if task.AssignedTo != nil {
		if next := h.bs.NextPendingTaskForSubscriber(r.Context(), project, *task.AssignedTo); next != nil {
			h.bs.PostMessage(r.Context(), project, "Coral Task Queue", h.buildAssignmentNotification(r.Context(), project, next, *task.AssignedTo, false), nil)
		}
	}
	if h.notifyFn != nil {
		h.notifyFn()
	}
	writeJSON(w, http.StatusOK, task)
}
