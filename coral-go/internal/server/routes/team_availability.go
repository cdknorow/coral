package routes

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

type availabilityTask struct {
	ID     int64  `json:"id"`
	Scope  string `json:"scope"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type availableAgent struct {
	SessionID               string                `json:"session_id,omitempty"`
	SubscriberID            string                `json:"subscriber_id,omitempty"`
	Name                    string                `json:"name"`
	AgentType               string                `json:"agent_type,omitempty"`
	Role                    string                `json:"role,omitempty"`
	Availability            string                `json:"availability"`
	Available               bool                  `json:"available"`
	Reason                  string                `json:"reason"`
	TaskState               string                `json:"task_state,omitempty"`
	Tasks                   []availabilityTask    `json:"tasks"`
	WaitingOn               *board.RegisteredWait `json:"waiting_on,omitempty"`
	Reminder                bool                  `json:"reminder,omitempty"`
	ReminderIntervalSeconds int                   `json:"reminder_interval_seconds,omitempty"`
	Activity                string                `json:"activity,omitempty"`
}

var statusActivityANSI = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)

// TeamAvailability reports observed runtime and queue state; it does not reserve work.
// GET /api/board/{project}/status
func (h *SessionsHandler) TeamAvailability(w http.ResponseWriter, r *http.Request) {
	if !boardStatusCacheable(r) {
		h.teamAvailabilityUncached(w, r)
		return
	}
	name := urlParam(r, "name")
	if name == "" {
		name = urlParam(r, "project")
	}
	// A sidebar render asks for a board's status three times at once, about 89 ms each
	// (#3721): those share one computation. Nothing is kept afterwards, so a status read after
	// a change is the fresh one. A request for agents' activity is the caller's own (authorized
	// per subscriber) and always computed for it.
	if h.boardStatus == nil {
		h.teamAvailabilityUncached(w, r)
		return
	}
	h.boardStatus.serve(w, r, name, func(rw http.ResponseWriter) { h.teamAvailabilityUncached(rw, r) })
}

// boardStatusCacheable: everything but a request for agents' activity (the caller's own).
func boardStatusCacheable(r *http.Request) bool {
	q := r.URL.Query().Get("activity")
	return q != "1" && !strings.EqualFold(q, "true")
}

// boardStatusSnapshot is what a board's status is computed from, read once per request.
type boardStatusSnapshot struct {
	sessions          []store.LiveSession
	subs              []board.Subscriber
	bySession         map[string]board.Subscriber // a subscriber by its session name
	tasks             []board.Task                // the board's open tasks
	underReview       map[int64]string            // task id -> assignee, for tasks waiting on their completion review
	live              map[string]bool             // session ids with a live terminal
	events            map[string][]store.AgentEvent
	waitsBySubscriber map[string]*board.RegisteredWait
	personalBySession map[string][]store.AgentTask
}

// statusError is a board status request that can't be answered: forbidden, or the server's fault.
type statusError struct {
	forbidden bool
	msg       string
}

func (e *statusError) write(w http.ResponseWriter) {
	if e.forbidden {
		errForbidden(w, e.msg)
		return
	}
	errInternalServer(w, e.msg)
}

func (h *SessionsHandler) teamAvailabilityUncached(w http.ResponseWriter, r *http.Request) {
	trace := newPhaseTrace("board-status")
	defer trace.finish()
	ctx := r.Context()
	name := urlParam(r, "name")
	if name == "" {
		name = urlParam(r, "project")
	}
	if h.bs == nil {
		errInternalServer(w, "board store unavailable")
		return
	}
	includeActivity := r.URL.Query().Get("activity") == "1" || strings.EqualFold(r.URL.Query().Get("activity"), "true")
	snap, serr := h.loadBoardStatus(r, trace, name, includeActivity, r.URL.Query().Get("subscriber_id"))
	if serr != nil {
		serr.write(w)
		return
	}
	agents := []availableAgent{}
	seen := map[string]bool{}
	for _, s := range snap.sessions {
		sub, subscribed := snap.bySession[s.AgentType+"-"+s.SessionID]
		if !subscribed && (s.BoardName == nil || *s.BoardName != name) {
			continue
		}
		if subscribed {
			seen[sub.SubscriberID] = true
		}
		agents = append(agents, h.liveAgentStatus(ctx, snap, name, s, sub, subscribed, includeActivity))
	}
	for _, sub := range snap.subs {
		if !seen[sub.SubscriberID] && !board.IsOperatorName(sub.SubscriberID) { // the person is not an agent: no card, no offline count, no reminder (#6382)
			agents = append(agents, h.offlineSubscriberStatus(snap, name, sub))
		}
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Available != agents[j].Available {
			return agents[i].Available
		}
		return agents[i].Name < agents[j].Name
	})
	mode, err := h.bs.GetWorkingMode(ctx, name)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// exists: the board has a subscriber, a message or an open task (#285), so the CLI can tell an absent board from an empty one.
	exists := len(agents) > 0 || len(snap.tasks) > 0
	if !exists {
		exists, _ = h.bs.BoardExists(ctx, name)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"board": name, "team": name, "exists": exists, "working_mode": mode, "observed_at": time.Now().UTC().Format(time.RFC3339), "agents": agents, "summary": availabilitySummary(agents), "unassigned_tasks": boardTasksAssignedTo(snap.tasks, ""), "health_report": availabilityHealthReport(snap.tasks)})
}

// loadBoardStatus reads the board's sessions, subscribers, tasks, terminals, state events, waits and personal tasks,
// in that order, each phase traced. A request for agents' activity must come from an active subscriber that may
// peek (or the Orchestrator, or the Operator): checked as soon as the subscribers are read.
func (h *SessionsHandler) loadBoardStatus(r *http.Request, trace *phaseTrace, name string, includeActivity bool, activityCaller string) (*boardStatusSnapshot, *statusError) {
	ctx := r.Context()
	internal := func(err error) *statusError { return &statusError{msg: err.Error()} }
	snap := &boardStatusSnapshot{}
	var err error
	trace.begin("db_sessions")
	if snap.sessions, err = h.ss.GetAllLiveSessions(ctx); err != nil {
		return nil, internal(err)
	}
	trace.endPool("db_sessions", h.db, "rows", len(snap.sessions))
	trace.begin("db_board_snapshot")
	if snap.subs, err = h.bs.ListSubscribers(ctx, name); err != nil {
		return nil, internal(err)
	}
	if includeActivity && !activityAuthorized(snap.subs, activityCaller) {
		return nil, &statusError{forbidden: true, msg: "activity context requires an active Orchestrator or Operator subscription"}
	}
	// Only open tasks matter here; see ListOpenTasks for why not ListTasks.
	if snap.tasks, err = h.bs.ListOpenTasks(ctx, name); err != nil {
		return nil, internal(err)
	}
	if snap.underReview, err = h.bs.TasksUnderReview(ctx, name); err != nil {
		return nil, internal(err)
	}
	trace.endPool("db_board_snapshot", h.db, "subscribers", len(snap.subs), "tasks", len(snap.tasks))
	trace.begin("terminal_discovery")
	runtime, err := h.discoverAgents(r)
	if err != nil {
		return nil, &statusError{msg: "cannot determine live agent availability"}
	}
	trace.end("terminal_discovery", "agents", len(runtime))
	snap.live = map[string]bool{}
	for _, a := range runtime {
		snap.live[a.SessionID] = true
	}
	snap.bySession = map[string]board.Subscriber{}
	for _, sub := range snap.subs {
		snap.bySession[sub.SessionName] = sub
	}
	ids := []string{}
	for _, s := range snap.sessions {
		if _, subscribed := snap.bySession[s.AgentType+"-"+s.SessionID]; subscribed || (s.BoardName != nil && *s.BoardName == name) {
			ids = append(ids, s.SessionID)
		}
	}
	trace.begin("state_events")
	if snap.events, err = h.ts.GetSessionStateEvents(ctx, ids); err != nil {
		return nil, internal(err)
	}
	trace.endPool("state_events", h.db, "sessions", len(ids))
	waits, _ := h.bs.ListActiveWaits(ctx, name)
	snap.waitsBySubscriber = make(map[string]*board.RegisteredWait)
	for i := range waits {
		snap.waitsBySubscriber[waits[i].SubscriberID] = &waits[i]
	}
	// Every listed agent's personal tasks in one query, not one per agent (#4154).
	if snap.personalBySession, err = h.ts.ListAgentTasksForSessions(ctx, ids); err != nil {
		return nil, internal(err)
	}
	return snap, nil
}

// activityAuthorized: caller is an active subscriber that may peek, the Orchestrator or the Operator.
func activityAuthorized(subs []board.Subscriber, caller string) bool {
	for _, sub := range subs {
		if sub.IsActive != 0 && sub.SubscriberID == caller && (sub.CanPeek != 0 || strings.EqualFold(strings.TrimSpace(sub.JobTitle), "Orchestrator") || strings.EqualFold(strings.TrimSpace(sub.SubscriberID), "Operator")) {
			return true
		}
	}
	return false
}

// liveAgentStatus is the status of an agent with a session on this board (subscribed or launched for it).
func (h *SessionsHandler) liveAgentStatus(ctx context.Context, snap *boardStatusSnapshot, name string, s store.LiveSession, sub board.Subscriber, subscribed, includeActivity bool) availableAgent {
	a := availableAgent{SessionID: s.SessionID, Name: s.AgentName, AgentType: s.AgentType, Tasks: []availabilityTask{}}
	if s.DisplayName != nil && *s.DisplayName != "" {
		a.Name = *s.DisplayName
	}
	if subscribed {
		a.SubscriberID = sub.SubscriberID
		a.Role = sub.JobTitle
	}
	if a.SubscriberID != "" && h.boardHandler != nil {
		a.ReminderIntervalSeconds, a.Reminder = h.boardHandler.SubscriberReminderInterval(name, a.SubscriberID)
	}
	// As ListAgentTasks(agent name, session id): this session's tasks under this agent's name.
	var personal []store.AgentTask
	for _, t := range snap.personalBySession[s.SessionID] {
		if t.AgentName == s.AgentName {
			personal = append(personal, t)
		}
	}
	for _, t := range personal {
		if openAvailabilityTask(t.Status) {
			a.Tasks = append(a.Tasks, availabilityTask{t.ID, "personal", t.Title, t.Status})
		}
	}
	if a.SubscriberID != "" {
		a.Tasks = append(a.Tasks, boardTasksAssignedTo(snap.tasks, a.SubscriberID)...)
	}
	if includeActivity && len(a.Tasks) > 0 && subscribed && sub.SessionName != "" && h.terminal != nil {
		captureCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		// The session's own pane (bySession matched it by type and id), through the shared,
		// stamp-keyed capture the poll and the login check use (#4077).
		output, captureErr := h.sharedCapture(captureCtx, sub.SessionName, 40, s.AgentType, s.SessionID)
		cancel()
		if captureErr == nil {
			a.Activity = latestStatusActivity(output)
		}
	}
	input := SessionStateInput{Sleeping: s.IsSleeping != 0}
	for _, e := range snap.events[s.SessionID] {
		input.Events = append(input.Events, e.StateEvent())
	}
	h.applyTranscriptState(&input, snap.events[s.SessionID], s.AgentType, naming.SessionName(s.AgentType, s.SessionID), s.SessionID, s.WorkingDir)
	var activeWait *board.RegisteredWait
	if a.SubscriberID != "" {
		activeWait = snap.waitsBySubscriber[a.SubscriberID]
	}
	a.TaskState = taskStateOf(a.Tasks, a.SubscriberID, personal, snap.underReview)
	state := DeriveSessionState(input)
	classifyAvailability(&a, s, snap.live[s.SessionID], subscribed && sub.IsActive != 0, state, len(input.Events) > 0, activeWait)
	// An agent parked on a wait while its task is in progress isn't idle: it said what it
	// waits for.
	if a.TaskState == "active" && activeWait == nil && !state.Working {
		a.TaskState = "idle"
	}
	return a
}

// offlineSubscriberStatus is the status of a subscriber with no session here: offline, remote (unknown), or waiting.
func (h *SessionsHandler) offlineSubscriberStatus(snap *boardStatusSnapshot, name string, sub board.Subscriber) availableAgent {
	a := availableAgent{SubscriberID: sub.SubscriberID, Name: sub.SubscriberID, Role: sub.JobTitle, Availability: "offline", Reason: "No registered local session", Tasks: []availabilityTask{}}
	if h.boardHandler != nil {
		a.ReminderIntervalSeconds, a.Reminder = h.boardHandler.SubscriberReminderInterval(name, sub.SubscriberID)
	}
	if sub.OriginServer != nil && *sub.OriginServer != "" {
		a.Availability = "unknown"
		a.Reason = "Remote agent availability is not observed locally"
	}
	a.Tasks = append(a.Tasks, boardTasksAssignedTo(snap.tasks, sub.SubscriberID)...)
	if wait := snap.waitsBySubscriber[sub.SubscriberID]; wait != nil {
		a.WaitingOn = wait
		a.Availability = "waiting"
		a.Reason = waitingReason(wait)
	}
	return a
}

// boardTasksAssignedTo is the open board tasks assigned to subscriber ("": the unassigned ones).
func boardTasksAssignedTo(tasks []board.Task, subscriber string) []availabilityTask {
	out := []availabilityTask{}
	for _, t := range tasks {
		assignee := ""
		if t.AssignedTo != nil {
			assignee = *t.AssignedTo
		}
		if assignee == subscriber && openAvailabilityTask(t.Status) {
			out = append(out, availabilityTask{t.ID, "board", t.Title, t.Status})
		}
	}
	return out
}

// taskStateOf is an agent's task state from its open tasks: "active" with one in progress (a board task waiting on
// its review doesn't count), else "blocked" with one blocked, else "awaiting-review" when one of its tasks really is
// under review (#3520, QA's bug bash F5: a registered wait alone, on a teammate's reply say, isn't a review), else "".
func taskStateOf(tasks []availabilityTask, subscriberID string, personal []store.AgentTask, underReview map[int64]string) string {
	state := ""
	for _, task := range tasks {
		if task.Status == "in_progress" && !(task.Scope == "board" && underReview[task.ID] != "") {
			state = "active"
		} else if state == "" && task.Status == "blocked" {
			state = "blocked"
		}
	}
	if state != "active" && state != "blocked" && hasTaskUnderReview(subscriberID, personal, underReview) {
		state = "awaiting-review"
	}
	return state
}

// availabilitySummary counts the agents by availability.
func availabilitySummary(agents []availableAgent) map[string]int {
	summary := map[string]int{"total": len(agents), "available": 0, "busy": 0, "task_idle": 0, "queued": 0, "needs_input": 0, "sleeping": 0, "archived": 0, "offline": 0, "unknown": 0, "unavailable": 0, "waiting": 0}
	for _, a := range agents {
		summary[a.Availability]++
	}
	return summary
}

// waitingReason is the line for an agent parked on a registered wait.
func waitingReason(wait *board.RegisteredWait) string {
	if wait.Reason != "" {
		return fmt.Sprintf("Waiting for %s (%s)", wait.TargetLabel(), wait.Reason)
	}
	return fmt.Sprintf("Waiting for %s", wait.TargetLabel())
}

func latestStatusActivity(raw string) string {
	clean := statusActivityANSI.ReplaceAllString(raw, "")
	clean = strings.ReplaceAll(clean, "\r", "\n")
	lines := strings.Split(clean, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		line = strings.ReplaceAll(line, "@", "(at)")
		if len(line) > 160 {
			line = line[:159] + "…"
		}
		return line
	}
	return ""
}

func availabilityHealthReport(tasks []board.Task) []string {
	var report []string
	report = append(report, openReviewLines(tasks, time.Now())...)
	assigned := map[string]int{}
	parallel := 0
	for _, t := range tasks {
		if t.AssignedTo != nil && *t.AssignedTo != "" && t.Status == "in_progress" {
			assigned[*t.AssignedTo]++
		}
		if t.Status == "pending" && t.AssignedTo == nil && len(t.BlockedBy) == 0 && !t.Parked(time.Now()) { // a parked task is set aside, not work to hand out (#5191)
			parallel++
		}
		if t.Status == "blocked" {
			if len(t.BlockedBy) == 0 {
				report = append(report, fmt.Sprintf("#%d is blocked but has no prerequisites", t.ID))
				continue
			}
			// "skipped" is the board's canceled state (the status CHECK has no
			// "canceled").
			allDone, canceled := true, false
			for _, d := range t.BlockedBy {
				if d.Status != "completed" && d.Status != "skipped" {
					allDone = false
				}
				canceled = canceled || d.Status == "skipped"
			}
			if allDone {
				report = append(report, fmt.Sprintf("#%d is blocked after all prerequisites resolved", t.ID))
			}
			if canceled {
				report = append(report, fmt.Sprintf("#%d depends on canceled work", t.ID))
			}
		}
	}
	if len(assigned) > 1 {
		vals := []int{}
		for _, n := range assigned {
			vals = append(vals, n)
		}
		sort.Ints(vals)
		if vals[len(vals)-1] >= vals[0]+2 {
			report = append(report, "Active assignment load is imbalanced")
		}
	}
	if parallel > 1 {
		report = append(report, fmt.Sprintf("%d unassigned tasks can potentially run in parallel", parallel))
	}
	if len(report) == 0 {
		return []string{"No queue health issues detected."}
	}
	return report
}

// hasTaskUnderReview reports whether one of the agent's personal tasks, or a board task
// assigned to its subscriber, is waiting for its completion review (#3520).
func hasTaskUnderReview(subscriberID string, personal []store.AgentTask, underReview map[int64]string) bool {
	for _, t := range personal {
		if t.Status == "review_pending" {
			return true
		}
	}
	for _, assignee := range underReview {
		if subscriberID != "" && assignee == subscriberID {
			return true
		}
	}
	return false
}

func openAvailabilityTask(status string) bool {
	return status == "pending" || status == "in_progress" || status == "blocked" || status == "draft"
}

func classifyAvailability(a *availableAgent, s store.LiveSession, online, subscribed bool, state SessionState, known bool, activeWait ...*board.RegisteredWait) {
	a.Available = false
	active, pending := false, false
	for _, t := range a.Tasks {
		active = active || t.Status == "in_progress"
		pending = pending || t.Status == "pending"
	}
	var wait *board.RegisteredWait
	if len(activeWait) > 0 && activeWait[0] != nil {
		wait = activeWait[0]
	}
	switch {
	case s.IsArchived != 0:
		// Never a candidate for work: tasks cannot be assigned to an archived agent.
		a.Availability = "archived"
		a.Reason = "Agent is archived; unarchive it before assigning work"
	case s.IsSleeping != 0:
		a.Availability = "sleeping"
		a.Reason = "Agent is sleeping"
	case !online:
		a.Availability = "offline"
		a.Reason = "No live terminal session"
	case s.AgentType == "terminal":
		a.Availability = "unavailable"
		a.Reason = "Terminal, not an agent"
	case s.BoardServer != nil && *s.BoardServer != "":
		a.Availability = "unknown"
		a.Reason = "Remote board tasks are not observed locally"
	case wait != nil:
		a.Availability = "waiting"
		a.WaitingOn = wait
		a.Reason = waitingReason(wait)
	case state.NeedsInput:
		a.Availability = "needs_input"
		a.Reason = "Waiting for user input or approval"
	case active && state.Working:
		a.Availability = "busy"
		a.Reason = "Has an in-progress task and is working"
	case active:
		a.Availability = "task_idle"
		a.Reason = "Agent has task but is idle"
	case state.Working:
		a.Availability = "busy"
		a.Reason = "Agent is working outside a claimed task"
	case pending:
		a.Availability = "queued"
		a.Reason = "Has ready assigned work"
	case !subscribed:
		a.Availability = "unavailable"
		a.Reason = "No active board subscription"
	case !known || !state.AwaitingUser:
		a.Availability = "unknown"
		a.Reason = "No confirmed idle signal"
	default:
		a.Availability = "available"
		a.Available = true
		a.Reason = "Idle with no active or ready assigned tasks"
	}
}

// openReviewLines are the health check's open reviews (#3725): each reviewer's review not yet
// answered, and each task in review its orchestrator hasn't completed or delegated, with age.
func openReviewLines(tasks []board.Task, now time.Time) []string {
	age := func(at string) string {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return "?"
		}
		return now.Sub(t).Round(time.Minute).String()
	}
	var lines []string
	for _, t := range tasks {
		w := t.Workflow
		switch {
		case w.ReviewOf > 0 && (t.Status == "pending" || t.Status == "in_progress") && t.AssignedTo != nil:
			lines = append(lines, fmt.Sprintf("%s has review #%d of #%d open (%s, %s)", *t.AssignedTo, t.ID, w.ReviewOf, t.Status, age(t.CreatedAt)))
		case t.Status == "review_pending" && t.AssignedTo != nil:
			open := len(w.ReviewRounds) > 0 && w.ReviewRounds[len(w.ReviewRounds)-1].Result == ""
			if !open {
				since := t.CreatedAt
				if w.CompletionReview != nil {
					since = w.CompletionReview.SubmittedAt
				}
				lines = append(lines, fmt.Sprintf("#%d is in review with %s, not yet completed or delegated (%s)", t.ID, *t.AssignedTo, age(since)))
			}
		}
	}
	return lines
}
