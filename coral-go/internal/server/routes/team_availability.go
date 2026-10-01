package routes

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
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
}

// TeamAvailability reports observed runtime and queue state; it does not reserve work.
func (h *SessionsHandler) TeamAvailability(w http.ResponseWriter, r *http.Request) {
	trace := newPhaseTrace("board-status")
	defer trace.finish()
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	if name == "" {
		name = chi.URLParam(r, "project")
	}
	if h.bs == nil {
		errInternalServer(w, "board store unavailable")
		return
	}
	trace.begin("db_sessions")
	sessions, err := h.ss.GetAllLiveSessions(ctx)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	trace.end("db_sessions", append([]any{"rows", len(sessions)}, dbPoolAttrs(h.db)...)...)
	trace.begin("db_board_snapshot")
	subs, err := h.bs.ListSubscribers(ctx, name)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	tasks, err := h.bs.ListTasks(ctx, name)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	trace.end("db_board_snapshot", append([]any{"subscribers", len(subs), "tasks", len(tasks)}, dbPoolAttrs(h.db)...)...)
	trace.begin("terminal_discovery")
	runtime, err := h.discoverAgents(r)
	if err != nil {
		errInternalServer(w, "cannot determine live agent availability")
		return
	}
	trace.end("terminal_discovery", "agents", len(runtime))
	live := map[string]bool{}
	for _, a := range runtime {
		live[a.SessionID] = true
	}
	bySession := map[string]board.Subscriber{}
	for _, sub := range subs {
		bySession[sub.SessionName] = sub
	}
	ids := []string{}
	for _, s := range sessions {
		if _, subscribed := bySession[s.AgentType+"-"+s.SessionID]; subscribed || (s.BoardName != nil && *s.BoardName == name) {
			ids = append(ids, s.SessionID)
		}
	}
	trace.begin("state_events")
	events, err := h.ts.GetSessionStateEvents(ctx, ids)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	trace.end("state_events", append([]any{"sessions", len(ids)}, dbPoolAttrs(h.db)...)...)
	waits, _ := h.bs.ListActiveWaits(ctx, name)
	waitsBySubscriber := make(map[string]*board.RegisteredWait)
	for i := range waits {
		waitsBySubscriber[waits[i].SubscriberID] = &waits[i]
	}
	agents := []availableAgent{}
	seen := map[string]bool{}
	for _, s := range sessions {
		sub, subscribed := bySession[s.AgentType+"-"+s.SessionID]
		if !subscribed && (s.BoardName == nil || *s.BoardName != name) {
			continue
		}
		a := availableAgent{SessionID: s.SessionID, Name: s.AgentName, AgentType: s.AgentType, Tasks: []availabilityTask{}}
		if s.DisplayName != nil && *s.DisplayName != "" {
			a.Name = *s.DisplayName
		}
		if subscribed {
			a.SubscriberID = sub.SubscriberID
			a.Role = sub.JobTitle
			seen[sub.SubscriberID] = true
		}
		if a.SubscriberID != "" && h.boardHandler != nil {
			a.ReminderIntervalSeconds, a.Reminder = h.boardHandler.SubscriberReminderInterval(name, a.SubscriberID)
		}
		personal, err := h.ts.ListAgentTasks(ctx, s.AgentName, &s.SessionID)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		for _, t := range personal {
			if openAvailabilityTask(t.Status) {
				a.Tasks = append(a.Tasks, availabilityTask{t.ID, "personal", t.Title, t.Status})
			}
		}
		for _, t := range tasks {
			if a.SubscriberID != "" && t.AssignedTo != nil && *t.AssignedTo == a.SubscriberID && openAvailabilityTask(t.Status) {
				a.Tasks = append(a.Tasks, availabilityTask{t.ID, "board", t.Title, t.Status})
			}
		}
		input := SessionStateInput{Sleeping: s.IsSleeping != 0}
		for _, e := range events[s.SessionID] {
			input.Events = append(input.Events, StateEvent{Type: e.EventType, Summary: e.Summary})
		}
		h.applyTranscriptState(&input, events[s.SessionID], s.AgentType, s.SessionID, s.WorkingDir)
		var activeWait *board.RegisteredWait
		if a.SubscriberID != "" {
			activeWait = waitsBySubscriber[a.SubscriberID]
		}
		for _, task := range a.Tasks {
			if task.Status == "in_progress" {
				a.TaskState = "active"
			} else if a.TaskState == "" && task.Status == "blocked" {
				a.TaskState = "blocked"
			}
		}
		if activeWait != nil {
			a.TaskState = "awaiting-review"
		}
		classifyAvailability(&a, s, live[s.SessionID], subscribed && sub.IsActive != 0, DeriveSessionState(input), len(input.Events) > 0, activeWait)
		if a.TaskState == "active" {
			state := DeriveSessionState(input)
			if !state.Working {
				a.TaskState = "idle"
			}
		}
		agents = append(agents, a)
	}
	for _, sub := range subs {
		if seen[sub.SubscriberID] {
			continue
		}
		a := availableAgent{SubscriberID: sub.SubscriberID, Name: sub.SubscriberID, Role: sub.JobTitle, Availability: "offline", Reason: "No registered local session", Tasks: []availabilityTask{}}
		if h.boardHandler != nil {
			a.ReminderIntervalSeconds, a.Reminder = h.boardHandler.SubscriberReminderInterval(name, sub.SubscriberID)
		}
		if sub.OriginServer != nil && *sub.OriginServer != "" {
			a.Availability = "unknown"
			a.Reason = "Remote agent availability is not observed locally"
		}
		for _, t := range tasks {
			if t.AssignedTo != nil && *t.AssignedTo == sub.SubscriberID && openAvailabilityTask(t.Status) {
				a.Tasks = append(a.Tasks, availabilityTask{t.ID, "board", t.Title, t.Status})
			}
		}
		activeWait := waitsBySubscriber[sub.SubscriberID]
		if activeWait != nil {
			a.WaitingOn = activeWait
			a.Availability = "waiting"
			target := activeWait.TargetLabel()
			if activeWait.Reason != "" {
				a.Reason = fmt.Sprintf("Waiting for %s (%s)", target, activeWait.Reason)
			} else {
				a.Reason = fmt.Sprintf("Waiting for %s", target)
			}
		}
		agents = append(agents, a)
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Available != agents[j].Available {
			return agents[i].Available
		}
		return agents[i].Name < agents[j].Name
	})
	summary := map[string]int{"total": len(agents), "available": 0, "busy": 0, "task_idle": 0, "queued": 0, "needs_input": 0, "sleeping": 0, "offline": 0, "unknown": 0, "unavailable": 0, "waiting": 0}
	for _, a := range agents {
		summary[a.Availability]++
	}
	unassigned := []availabilityTask{}
	for _, t := range tasks {
		if (t.AssignedTo == nil || *t.AssignedTo == "") && openAvailabilityTask(t.Status) {
			unassigned = append(unassigned, availabilityTask{t.ID, "board", t.Title, t.Status})
		}
	}
	mode, err := h.bs.GetWorkingMode(ctx, name)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"board": name, "team": name, "working_mode": mode, "observed_at": time.Now().UTC().Format(time.RFC3339), "agents": agents, "summary": summary, "unassigned_tasks": unassigned, "health_report": availabilityHealthReport(tasks)})
}

func availabilityHealthReport(tasks []board.Task) []string {
	var report []string
	assigned := map[string]int{}
	parallel := 0
	for _, t := range tasks {
		if t.AssignedTo != nil && *t.AssignedTo != "" && t.Status == "in_progress" {
			assigned[*t.AssignedTo]++
		}
		if t.Status == "pending" && t.AssignedTo == nil && len(t.BlockedBy) == 0 {
			parallel++
		}
		if t.Status == "blocked" {
			allDone, canceled := true, false
			for _, d := range t.BlockedBy {
				if d.Status != "completed" && d.Status != "skipped" {
					allDone = false
				}
				canceled = canceled || d.Status == "canceled"
			}
			if allDone {
				report = append(report, fmt.Sprintf("#%d is blocked after all prerequisites completed", t.ID))
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
		target := wait.TargetLabel()
		if wait.Reason != "" {
			a.Reason = fmt.Sprintf("Waiting for %s (%s)", target, wait.Reason)
		} else {
			a.Reason = fmt.Sprintf("Waiting for %s", target)
		}
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
