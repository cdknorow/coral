package routes

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/board"
)

// Goals on the board (#7087, from #7078's note). A goal is an outcome a board's tasks work toward; the store is internal/board/goals.go. These routes write board text and nothing else: they never
// start, wake, resume or message an agent (the one side effect a task made under a goal keeps is the nudge to an awake assignee that every task has), which is what lets Coral Hub offer them at
// Interact. Who may write: the Operator and an active registered orchestrator (the people who plan a board's tasks); anyone who reads the board may read goals.
//
//	POST  /api/board/{project}/goals                 {title, outcome?, done_when?, state?, driver?, subscriber_id}          -> 201 the goal
//	GET   /api/board/{project}/goals?state=a,b       default: proposed, active, blocked                                      -> {"goals": [...]}
//	GET   /api/board/{project}/goals/{goalID}                                                                                -> the goal
//	PATCH /api/board/{project}/goals/{goalID}        any of title, outcome, done_when, state, blocked_on, driver, decision,
//	                                                 recommendation, if_silent, with subscriber_id                           -> the goal
//	POST  /api/board/{project}/goals/{goalID}/close  {reason, state? done|dropped, subscriber_id}                            -> {"goal": ..., "open_tasks": n}
//	PUT   /api/board/{project}/tasks/{taskID}/goal   {goal_id: n | null, subscriber_id}                                      -> the task
//	GET   /api/board/{project}/tasks?goal=<id>       the goal's tasks, as the usual task page

// goalError answers a goal refusal with its fixed word and says it did: 404 for a goal or task that is not there (another board's goal is not there), 409 for a closed goal, 400 for the rest.
func goalError(w http.ResponseWriter, err error) bool {
	var ge *board.GoalError
	if !errors.As(err, &ge) {
		return false
	}
	status := http.StatusBadRequest
	switch ge.Code {
	case board.ErrGoalNotFound.Code, board.ErrGoalTaskNotFound.Code:
		status = http.StatusNotFound
	case board.ErrGoalClosed.Code, board.ErrGoalAlreadyClosed.Code, board.ErrGoalNotClosed.Code:
		status = http.StatusConflict
	}
	writeError(w, status, ge.Code, ge.Message)
	return true
}

// requireGoalPlanner is requireTaskPlanner for a goal write, in a goal's words: the Operator, or an active registered orchestrator.
func (h *BoardHandler) requireGoalPlanner(w http.ResponseWriter, r *http.Request, project, actor string) bool {
	if callerActor(r).IsOperator() {
		return true
	}
	if actor == "Operator" {
		if identityActorEnforced() {
			errForbidden(w, "only the Operator or an active registered orchestrator may create or change goals")
			return false
		}
		noteEnforceDiffers("goal-planner", r)
		return true
	}
	if err := h.bs.RequireTaskReviewer(r.Context(), project, actor); err != nil {
		errForbidden(w, "only Operator or an active registered orchestrator may create or change goals; coordinate with your orchestrator")
		return false
	}
	return true
}

// goalActor is who writes a goal: the name the request claims, as the identity layer verified it, and allowed to plan the board. It answers the refusal itself.
func (h *BoardHandler) goalActor(w http.ResponseWriter, r *http.Request, project, claim string) (string, bool) {
	who, ok := attributedTo(w, r, claim)
	if !ok {
		return "", false
	}
	actor := strings.TrimSpace(claim)
	if identityActorEnforced() {
		actor = who
	}
	if actor == "" {
		errBadRequest(w, "subscriber_id required")
		return "", false
	}
	if !h.requireGoalPlanner(w, r, project, actor) {
		return "", false
	}
	return actor, true
}

func goalID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "goalID"), 10, 64)
	if err != nil || id <= 0 {
		errBadRequest(w, "invalid goal ID")
		return 0, false
	}
	return id, true
}

// CreateGoal: POST /api/board/{project}/goals
func (h *BoardHandler) CreateGoal(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	var body struct {
		Title        string `json:"title"`
		Outcome      string `json:"outcome"`
		DoneWhen     string `json:"done_when"`
		State        string `json:"state"`
		Driver       string `json:"driver"`
		SubscriberID string `json:"subscriber_id"`
		CreatedBy    string `json:"created_by"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if refuseNewBoardName(w, r, h.bs, project) {
		return
	}
	claim := body.SubscriberID
	if claim == "" {
		claim = body.CreatedBy
	}
	actor, ok := h.goalActor(w, r, project, claim)
	if !ok {
		return
	}
	g, err := h.bs.CreateGoal(r.Context(), project, board.GoalInput{Title: body.Title, Outcome: body.Outcome, DoneWhen: body.DoneWhen, State: body.State, Driver: body.Driver, CreatedBy: actor})
	if err != nil {
		if !goalError(w, err) {
			errInternalServer(w, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// ListGoals: GET /api/board/{project}/goals?state=a,b&limit=n
func (h *BoardHandler) ListGoals(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	q := board.GoalListQuery{}
	if s := strings.TrimSpace(r.URL.Query().Get("state")); s != "" {
		q.States = strings.Split(s, ",")
		for i := range q.States {
			q.States[i] = strings.TrimSpace(q.States[i])
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			errBadRequest(w, "limit must be a positive number")
			return
		}
		q.Limit = n
	}
	goals, err := h.bs.ListGoals(r.Context(), project, q)
	if err != nil {
		if !goalError(w, err) {
			errInternalServer(w, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"goals": goals})
}

// GetGoal: GET /api/board/{project}/goals/{goalID}
func (h *BoardHandler) GetGoal(w http.ResponseWriter, r *http.Request) {
	id, ok := goalID(w, r)
	if !ok {
		return
	}
	g, err := h.bs.GetGoal(r.Context(), urlParam(r, "project"), id)
	if err != nil {
		if !goalError(w, err) {
			errInternalServer(w, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// UpdateGoal: PATCH /api/board/{project}/goals/{goalID}. Only the fields the body names change; "driver": "" clears the driver.
func (h *BoardHandler) UpdateGoal(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	id, ok := goalID(w, r)
	if !ok {
		return
	}
	var body struct {
		Title          *string `json:"title"`
		Outcome        *string `json:"outcome"`
		DoneWhen       *string `json:"done_when"`
		State          *string `json:"state"`
		BlockedOn      *string `json:"blocked_on"`
		Driver         *string `json:"driver"`
		Decision       *string `json:"decision"`
		Recommendation *string `json:"recommendation"`
		IfSilent       *string `json:"if_silent"`
		SubscriberID   string  `json:"subscriber_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if _, ok := h.goalActor(w, r, project, body.SubscriberID); !ok {
		return
	}
	g, err := h.bs.UpdateGoal(r.Context(), project, id, board.GoalUpdate{Title: body.Title, Outcome: body.Outcome, DoneWhen: body.DoneWhen, State: body.State, BlockedOn: body.BlockedOn,
		Driver: body.Driver, Decision: body.Decision, Recommendation: body.Recommendation, IfSilent: body.IfSilent})
	if err != nil {
		if !goalError(w, err) {
			errInternalServer(w, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// CloseGoal: POST /api/board/{project}/goals/{goalID}/close. A goal with open tasks is closed all the same: the answer says how many were open, so the caller can warn.
func (h *BoardHandler) CloseGoal(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	id, ok := goalID(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason       string `json:"reason"`
		State        string `json:"state"`
		SubscriberID string `json:"subscriber_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if _, ok := h.goalActor(w, r, project, body.SubscriberID); !ok {
		return
	}
	g, open, err := h.bs.CloseGoal(r.Context(), project, id, body.Reason, body.State)
	if err != nil {
		if !goalError(w, err) {
			errInternalServer(w, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"goal": g, "open_tasks": open})
}

// SetTaskGoal: PUT /api/board/{project}/tasks/{taskID}/goal {"goal_id": n | null}. null unlinks; a goal that is not on this board, or is closed, is refused.
func (h *BoardHandler) SetTaskGoal(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil || taskID <= 0 {
		errBadRequest(w, "invalid task ID")
		return
	}
	var body struct {
		GoalID       json.RawMessage `json:"goal_id"`
		SubscriberID string          `json:"subscriber_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if len(body.GoalID) == 0 {
		errBadRequest(w, "goal_id is required: a goal's number, or null to unlink")
		return
	}
	var goal *int64
	if string(body.GoalID) != "null" {
		var n int64
		if err := json.Unmarshal(body.GoalID, &n); err != nil || n <= 0 {
			errBadRequest(w, "goal_id must be a goal's number, or null")
			return
		}
		goal = &n
	}
	if _, ok := h.goalActor(w, r, project, body.SubscriberID); !ok {
		return
	}
	if err := h.bs.SetTaskGoal(r.Context(), project, taskID, goal); err != nil {
		if !goalError(w, err) {
			errInternalServer(w, err.Error())
		}
		return
	}
	task, err := h.bs.GetTask(r.Context(), project, taskID)
	if err != nil {
		errTaskStore(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// goalTaskPage is the task page of one goal: the usual page, and truncated when the goal has more tasks than one answer holds (board.MaxGoalTasks).
type goalTaskPage struct {
	board.TaskListPage
	Truncated bool `json:"truncated,omitempty"`
}

// listGoalTasks answers GET .../tasks?goal=<id> with a goal's tasks in the shape of the task page, whole in one answer (a goal's tasks are bounded).
func (h *BoardHandler) listGoalTasks(w http.ResponseWriter, r *http.Request, project, raw string) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		errBadRequest(w, "goal must be a goal's number")
		return
	}
	rows, truncated, err := h.bs.GoalTasks(r.Context(), project, id)
	if err != nil {
		if !goalError(w, err) {
			errInternalServer(w, err.Error())
		}
		return
	}
	page := goalTaskPage{TaskListPage: board.TaskListPage{Tasks: rows}, Truncated: truncated}
	for _, t := range rows {
		switch t.Status {
		case "completed", "skipped":
			page.TotalCompleted++
		case "pending", "in_progress", "blocked", "draft", "review_pending":
			page.TotalOpen++
		}
	}
	writeJSON(w, http.StatusOK, page)
}
