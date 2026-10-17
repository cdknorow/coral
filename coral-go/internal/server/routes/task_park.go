package routes

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cliauth"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/teamhealth"
)

// maxParkDays is the furthest a park's end may be.
const maxParkDays = 90

// healthAdmin says who may park, unpark and ack on project (Security's #4176 rule 19): the
// Operator (no agent token; an agent's command line only with the Operator's signature) or the
// team's orchestrator, known by its own agent token's subscription on this board, never by a
// name the request gives. An agent can't park or ack its own task to hide that it is stuck.
func (h *BoardHandler) healthAdmin(r *http.Request, project string) (who string, ok bool) {
	// Never through Coral Hub: these routes are off the tunnel table, and this is the second lock
	// should one ever be added (Security's SHOULD on #4225).
	if cloud.Tunneled(r) {
		return "", false
	}
	// By the actor alone (#4255): the Operator the identity layer verified, or the member the token's
	// session holds on this board when that member is the team's orchestrator. A claimed actor is
	// neither. In log mode the earlier decision stands, and where the two differ it is counted.
	byActor, actorOK := h.healthAdminByActor(r, project)
	if identityActorEnforced() {
		return byActor, actorOK
	}
	who, ok = h.healthAdminLegacy(r, project)
	if ok != actorOK || who != byActor {
		noteEnforceDiffers("health-admin", r)
	}
	return who, ok
}

func (h *BoardHandler) healthAdminByActor(r *http.Request, project string) (string, bool) {
	a := callerActor(r)
	if name, member := a.Member(); member {
		orch, err := h.bs.TeamOrchestrator(r.Context(), project)
		if err != nil || !board.SameIdentity(orch, name) {
			return "", false
		}
		return name, true
	}
	if a.Kind == ActorOperator {
		return "Operator", true
	}
	return "", false
}

// healthAdminLegacy is the decision before the actor: the token's own subscription lookup, or the
// absence of an agent token (an agent tool only with the Operator's checked signature).
func (h *BoardHandler) healthAdminLegacy(r *http.Request, project string) (who string, ok bool) {
	if owner, verified := VerifiedSession(r.Context()); verified {
		sub, err := h.bs.GetSubscriptionBySessionName(r.Context(), owner.SessionName)
		if err != nil || sub == nil || sub.Project != project {
			return "", false
		}
		orch, err := h.bs.TeamOrchestrator(r.Context(), project)
		if err != nil || !board.SameIdentity(orch, sub.SubscriberID) {
			return "", false
		}
		return sub.SubscriberID, true
	}
	if isAgentTool(r) && !cliauth.Verified(r) {
		return "", false
	}
	return "Operator", true
}

// ParkTask parks a task: POST /api/board/{project}/tasks/{taskID}/park {"reason", "until"}
// (until: RFC 3339, optional). DELETE unparks it. The orchestrator or the Operator only.
func (h *BoardHandler) ParkTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil || taskID <= 0 {
		errBadRequest(w, "invalid task ID")
		return
	}
	who, ok := h.healthAdmin(r, project)
	if !ok {
		errForbidden(w, "only the team's orchestrator or the Operator can park or unpark a task")
		return
	}
	if r.Method == http.MethodDelete {
		task, err := h.bs.UnparkTask(r.Context(), project, taskID)
		if errors.Is(err, sql.ErrNoRows) {
			errNotFound(w, "Task not found")
			return
		} else if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		h.bs.PostSystemMessage(r.Context(), project, "Coral Task Queue", fmt.Sprintf("[Task #%d unparked by %s]", taskID, teamhealth.Name(who)), nil)
		writeJSON(w, http.StatusOK, task)
		return
	}
	var body struct {
		Reason string `json:"reason"`
		Until  string `json:"until"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if len(body.Reason) > 500 {
		errBadRequest(w, "reason: at most 500 characters")
		return
	}
	// The reason is shown later (task detail, the check), so it is stored cleaned (#4176 rule 2):
	// no invisible or bidi characters, no secret, no @, one line.
	reason := teamhealth.StoredText(body.Reason, nil)
	if strings.TrimSpace(reason) == "" {
		errBadRequest(w, "a reason is required")
		return
	}
	var until time.Time
	if body.Until != "" {
		until, err = time.Parse(time.RFC3339, body.Until)
		if err != nil {
			errBadRequest(w, "until: an RFC 3339 time")
			return
		}
		if now := time.Now(); !until.After(now) || until.After(now.AddDate(0, 0, maxParkDays)) {
			errBadRequest(w, fmt.Sprintf("until: a time in the next %d days", maxParkDays))
			return
		}
	}
	task, err := h.bs.ParkTask(r.Context(), project, taskID, reason, until)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		errNotFound(w, "Task not found")
		return
	case errors.Is(err, board.ErrNotParkable):
		errBadRequest(w, err.Error())
		return
	case err != nil:
		errInternalServer(w, err.Error())
		return
	}
	// The board line names who parked it and until when; the reason (text someone wrote) stays
	// on the task (#4176 rule 1).
	end := ""
	if !until.IsZero() {
		end = " until " + until.Local().Format("2006-01-02 15:04")
	}
	h.bs.PostSystemMessage(r.Context(), project, "Coral Task Queue", fmt.Sprintf("[Task #%d parked by %s%s] no reminders or nudges for it until then", taskID, teamhealth.Name(who), end), nil)
	writeJSON(w, http.StatusOK, task)
}
