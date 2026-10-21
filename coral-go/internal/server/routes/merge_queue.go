package routes

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/board"
)

// withVerifiedCaller is the name a request that claims none acts as (#5793, the same rule as a task reply, #5779): in log mode attributedTo returns the claim, which a
// dashboard button does not send, so the verified actor's own name stands in, in either identity mode. Never a name the body gives, and an unverified caller still has
// none (the handler refuses it); through Coral Hub the tunnel's guard names the caller, so that path is left as it was.
func withVerifiedCaller(r *http.Request, attributed string) string {
	if attributed != "" || callerActor(r).Kind == ActorHub {
		return attributed
	}
	if name, verified := actorName(callerActor(r)); verified {
		return name
	}
	return attributed
}

// AttachPatches adds patch rows to a task that is in review with an open round (#5689): the author's next revision of a patch or a
// reviewer's fix commit. When a reviewer had already approved, the round is superseded and the same reviewers review the new bytes.
// POST /api/board/{project}/tasks/{taskID}/attach {subscriber_id, artifacts}
func (h *BoardHandler) AttachPatches(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	id, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		SubscriberID string               `json:"subscriber_id"`
		Artifacts    []board.TaskArtifact `json:"artifacts"`
	}
	derr := decodeJSON(r, &body)
	if derr == nil {
		attrWho, attrOK := attributedTo(w, r, body.SubscriberID)
		if !attrOK {
			return
		}
		body.SubscriberID = withVerifiedCaller(r, attrWho)
	}
	if derr != nil || body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id and valid JSON required")
		return
	}
	unverified := h.vetTaskArtifacts(r, project, body.Artifacts)
	task, reopened, err := h.bs.AttachPatches(r.Context(), project, id, body.SubscriberID, body.Artifacts)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "is not on this board"):
			errNotFound(w, err.Error())
		case strings.Contains(err.Error(), "only the author, a reviewer of the round or the orchestrator"):
			errForbidden(w, err.Error())
		default:
			errBadRequest(w, err.Error())
		}
		return
	}
	if reopened != nil {
		var mentions []string
		for _, rv := range reopened.Reviewers {
			mentions = append(mentions, "@"+rv.Reviewer)
		}
		h.bs.PostSystemMessage(r.Context(), project, "Coral Task Queue", fmt.Sprintf("%s [Task #%d has new patch revisions from %s, round %d] an approval of the earlier bytes does not carry over: review it again (coral-board task detail %d), then coral-board task approve %d --round %d or coral-board task reject %d --round %d --note \"...\"",
			strings.Join(mentions, " "), id, body.SubscriberID, reopened.Round, id, id, reopened.Round, id, reopened.Round), nil)
	}
	if h.notifyFn != nil {
		h.notifyFn(project)
	}
	writeTaskVetted(w, map[string]any{"task": task, "new_round": reopened}, unverified)
}

// SendBack returns an approved, completed task to its author with a note (#5689): what a fix after approval, or a patch the queue
// cannot apply, uses. Only an orchestrator.
// POST /api/board/{project}/tasks/{taskID}/send-back {subscriber_id, note}
func (h *BoardHandler) SendBack(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	id, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		Note         string `json:"note"`
	}
	derr := decodeJSON(r, &body)
	if derr == nil {
		attrWho, attrOK := attributedTo(w, r, body.SubscriberID)
		if !attrOK {
			return
		}
		body.SubscriberID = withVerifiedCaller(r, attrWho)
	}
	if derr != nil || body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id and valid JSON required")
		return
	}
	task, err := h.bs.SendBack(r.Context(), project, id, body.SubscriberID, body.Note)
	if err != nil {
		if strings.Contains(err.Error(), "orchestrator is required") {
			errForbidden(w, err.Error())
		} else {
			errBadRequest(w, err.Error())
		}
		return
	}
	author := ""
	if task.AssignedTo != nil {
		author = *task.AssignedTo
	}
	h.bs.PostSystemMessage(r.Context(), project, "Coral Task Queue", fmt.Sprintf("@%s [Task #%d sent back by %s] %s. Fix it and submit-review again: the same reviewers review the new revision (the full note: coral-board task detail %d)", author, id, body.SubscriberID, oneLine(body.Note, 300), id), nil)
	h.bs.PostSystemMessage(r.Context(), project, "Coral Task Queue", h.buildAssignmentNotification(r.Context(), project, task, author, true), nil)
	if h.notifyFn != nil {
		h.notifyFn(project)
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

// oneLine is note on one line, cut to max characters: what a board post shows of a long note (the whole note stays on the task).
func oneLine(note string, max int) string {
	line := strings.Join(strings.Fields(note), " ")
	if r := []rune(line); len(r) > max {
		return string(r[:max]) + "..."
	}
	return line
}

// MergeQueue lists the approved, not yet landed tasks of a board (#5689): the newest row of each patch name that its approving round
// approved, oldest approval first. A read of what the board holds, never in the tunnel (Security's decision, #5723).
// GET /api/board/{project}/queue
func (h *BoardHandler) MergeQueue(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	h.setBoardIdentity(w, r, project)
	q, err := h.bs.MergeQueue(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": q})
}

// MarkLanded records that patches of an approved, completed task landed, and the commits that landed them (#5689). Only the board's
// orchestrator or the Operator, bound to the caller's identity like attach: a task's author marking his own patch landed would hide it
// from the queue. A digest the approving round did not approve is refused.
// POST /api/board/{project}/tasks/{taskID}/mark-landed {subscriber_id, digests, commits: [{sha, subject}]}
func (h *BoardHandler) MarkLanded(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	id, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		SubscriberID string               `json:"subscriber_id"`
		Digests      []string             `json:"digests"`
		Commits      []board.LandedCommit `json:"commits"`
	}
	derr := decodeJSON(r, &body)
	if derr == nil {
		attrWho, attrOK := attributedTo(w, r, body.SubscriberID)
		if !attrOK {
			return
		}
		body.SubscriberID = withVerifiedCaller(r, attrWho)
	}
	if derr != nil {
		errBadRequest(w, "valid JSON required")
		return
	}
	byOperator := callerActor(r).Kind == ActorOperator && (strings.TrimSpace(body.SubscriberID) == "" || board.IsOperatorName(body.SubscriberID))
	if byOperator && strings.TrimSpace(body.SubscriberID) == "" {
		body.SubscriberID = board.OperatorName
	}
	if body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	task, err := h.bs.MarkLanded(r.Context(), project, id, body.SubscriberID, byOperator, body.Digests, body.Commits)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "orchestrator is required"):
			errForbidden(w, err.Error())
		case strings.Contains(err.Error(), "is not on this board"):
			writeError(w, http.StatusNotFound, "not_found", err.Error())
		default:
			errBadRequest(w, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}
