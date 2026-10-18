package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
)

// The outcomes the UpdateTask oracle must reach, each counted from the condition happening, with a floor.
var updateTaskOutcomes = map[string]int{
	"invalid task id":                            10,
	"invalid JSON":                               10,
	"title too large":                            10,
	"body refused (amend)":                       10,
	"removing blockers mixed with other edits":   20,
	"not a planner (assign or remove blockers)":  20,
	"title or priority of a missing task":        10,
	"title or priority by a non-assignee":        20,
	"title or priority without an actor":         10,
	"archived assignee":                          10,
	"bad blocked_by":                             10,
	"the store refused":                          20,
	"identity unverified (enforce)":              10,
	"edited":                                     150,
	"blocked, assignee told":                     10,
	"blocked, no assignee":                       5,
	"unblocked, assignee told and nudged":        10,
	"unblocked, no assignee":                     5,
	"reassigned, new assignee nudged":            20,
	"reassigned, new assignee busy (no nudge)":   5,
	"reassigned and unblocked, nudged once":      3,
	"reassigned to the same assignee (no nudge)": 5,
}

var oracleStamp = regexp.MustCompile(`\d{4}-\d\d-\d\d[T ][0-9:.]+(Z|[+-]\d\d:\d\d)?`)

// #4756: UpdateTask split into allowTaskEdit, applyTaskEdit and announceTaskEdit. Two handlers, each with its own
// board and terminal, take the same random edits in lockstep: the old handler and the new one answer the same,
// leave the same tasks and board messages, and type the same nudges.
func TestUpdateTaskAgreesWithBefore4756(t *testing.T) {
	prevEnforced := identityActorEnforced()
	t.Cleanup(func() { setIdentityActorEnforced(prevEnforced) })
	counts := map[string]int{}
	var countsMu sync.Mutex
	// The 60 seeds share nothing but the process-wide enforce flag (updateOracleFlagMu holds it for one request on each side), so they run
	// together; the parent test waits for them all before it checks the floors (#4852).
	t.Run("seeds", func(t *testing.T) {
		for seed := int64(1); seed <= 60; seed++ {
			t.Run(fmt.Sprint(seed), func(t *testing.T) {
				t.Parallel()
				mine := map[string]int{}
				runUpdateTaskOracle(t, seed, mine)
				countsMu.Lock()
				for k, v := range mine {
					counts[k] += v
				}
				countsMu.Unlock()
			})
		}
	})
	t.Logf("compared, per outcome: %v", counts)
	for name, floor := range updateTaskOutcomes {
		require.GreaterOrEqual(t, counts[name], floor, "%q went (almost) unexercised", name)
	}
}

// updateOracleFlagMu serialises the one request per side that depends on the process-wide enforce flag.
var updateOracleFlagMu sync.Mutex

func runUpdateTaskOracle(t *testing.T, seed int64, counts map[string]int) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	members := []struct{ id, title, session string }{
		{"Orch", "Orchestrator", "claude-00000001-4756-4756-8756-000000000001"},
		{"Dev", "Developer", "claude-00000002-4756-4756-8756-000000000002"},
		{"QA", "QA Engineer", "claude-00000003-4756-4756-8756-000000000003"},
		{"Arch", "Developer", "claude-00000004-4756-4756-8756-000000000004"}, // archived
		{"Idle", "Developer", ""}, // no session
	}
	type side struct {
		h    *BoardHandler
		term *mockSessionTerminal
		mux  http.Handler
	}
	var sides [2]side
	for i := range sides {
		_, h := setupBoardTestServer(t)
		term := newMockTerminal()
		for _, m := range members {
			if m.session != "" {
				term.addSession(m.session, "/tmp/w")
			}
			_, err := h.bs.Subscribe(ctx, "p", m.id, m.title, m.session, nil, "")
			require.NoError(t, err)
		}
		h.SetTerminal(term)
		h.isArchivedSession = func(_ context.Context, name string) bool { return name == members[3].session }
		mux := chi.NewRouter()
		if i == 0 {
			mux.Patch("/api/board/{project}/tasks/{taskID}", h.updateTaskBefore4756)
		} else {
			mux.Patch("/api/board/{project}/tasks/{taskID}", h.UpdateTask)
		}
		sides[i] = side{h, term, mux}
	}
	// The same tasks on both boards: some assigned, some claimed (in progress), one done, some blocked.
	assignees := []string{"", "Dev", "QA", "Idle", "Orch"}
	for i := 0; i < 8; i++ {
		who := assignees[rng.Intn(len(assignees))]
		claim, done := rng.Intn(4) == 0 && who != "", i == 7
		var blockers []board.TaskDep
		if i > 1 && rng.Intn(3) == 0 {
			blockers = []board.TaskDep{{TaskID: int64(1 + rng.Intn(i)), BoardID: "p"}}
		}
		for _, s := range sides {
			var task *board.Task
			var err error
			if who == "" {
				task, err = s.h.bs.CreateTask(ctx, "p", fmt.Sprintf("task %d", i), "do it", "medium", "Orch")
			} else {
				task, err = s.h.bs.CreateTask(ctx, "p", fmt.Sprintf("task %d", i), "do it", "medium", "Orch", who)
			}
			require.NoError(t, err)
			if blockers != nil && !claim {
				_, _, err = s.h.bs.UpdateTask(ctx, "p", task.ID, board.TaskUpdate{BlockedBy: &blockers}, 32)
				require.NoError(t, err)
			}
			if claim || done {
				claimer := who
				if claimer == "" {
					claimer = "QA"
				}
				if _, err := s.h.bs.ClaimTask(ctx, "p", claimer, task.ID); err == nil && done {
					_, err = s.h.bs.CompleteTask(ctx, "p", task.ID, claimer, nil)
					require.NoError(t, err)
				}
			}
		}
	}

	actors := []string{"Orch", "Orch", "Dev", "QA", "Operator", "", "Ghost", "Arch"}
	targets := []string{"Dev", "QA", "Arch", "Orch", "Ghost", "", "Idle", "Dev"}
	for step := 0; step < 80; step++ {
		enforced := rng.Intn(6) == 0
		caller := []Actor{{Kind: ActorClaimed}, {Kind: ActorOperator, Name: "Operator"}, {Kind: ActorToken, Name: "Dev"}}[rng.Intn(3)]
		id := fmt.Sprint(1 + rng.Intn(9)) // 9 is not a task
		if rng.Intn(25) == 0 {
			id = "abc"
		}
		body := map[string]any{"subscriber_id": actors[rng.Intn(len(actors))]}
		blocking := false
		switch rng.Intn(7) {
		case 0: // remove blockers, sometimes mixed with other edits
			if rng.Intn(2) == 0 {
				body["remove_blocker"] = []int{1 + rng.Intn(8), 0, -1}[rng.Intn(3)]
			}
			if rng.Intn(2) == 0 || body["remove_blocker"] == nil {
				body["clear_blockers"] = true
			}
		case 1, 2:
			blocking = true
			body["blocked_by"] = []any{
				[]int{1 + rng.Intn(8)}, []int{}, []int{1 + rng.Intn(8), 1 + rng.Intn(8)},
				[]map[string]any{{"task_id": 1 + rng.Intn(8)}}, "bad", []map[string]any{{"task_id": 1, "board_id": "elsewhere"}},
			}[rng.Intn(6)]
		}
		if rng.Intn(3) == 0 {
			body["assigned_to"] = targets[rng.Intn(len(targets))]
		}
		if rng.Intn(4) == 0 {
			body["title"] = []string{"renamed", "", strings.Repeat("x", 513)}[rng.Intn(3)]
		}
		if rng.Intn(5) == 0 {
			body["priority"] = []string{"high", "low", "urgent!"}[rng.Intn(3)]
		}
		if rng.Intn(20) == 0 {
			body["body"] = "new body"
		}
		raw, _ := json.Marshal(body)
		if rng.Intn(30) == 0 {
			raw = []byte("{not json")
		}
		tasksBefore, err := sides[1].h.bs.ListTasks(ctx, "p")
		require.NoError(t, err)
		nudgesBefore := map[string]int{} // per session, claim nudges for the task this step edits
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			for _, m := range members {
				nudgesBefore[m.session] = claimNudges(sides[1].term, m.session, n)
			}
		}

		type result struct {
			code     int
			body     string
			tasks    string
			messages []string
			nudges   map[string][]string
		}
		var results [2]result
		for i, s := range sides {
			req := httptest.NewRequest(http.MethodPatch, "/api/board/p/tasks/"+id, bytes.NewReader(raw))
			req = withActor(req, caller)
			rec := httptest.NewRecorder()
			updateOracleFlagMu.Lock() // the enforce flag is the process's: set, serve and let the background work finish under one hold
			setIdentityActorEnforced(enforced)
			s.mux.ServeHTTP(rec, req)
			s.h.WaitBackground()
			updateOracleFlagMu.Unlock()
			tasks, err := s.h.bs.ListTasks(ctx, "p")
			require.NoError(t, err)
			tj, _ := json.Marshal(tasks)
			msgs, err := s.h.bs.ListMessages(ctx, "p", 1000, 0, 0)
			require.NoError(t, err)
			var texts []string
			for _, m := range msgs {
				texts = append(texts, m.SubscriberID+": "+m.Content)
			}
			s.term.mu.Lock()
			nudges := map[string][]string{}
			for k, v := range s.term.sent {
				nudges[k] = append([]string(nil), v...)
			}
			s.term.mu.Unlock()
			results[i] = result{rec.Code, oracleStamp.ReplaceAllString(rec.Body.String(), "<t>"),
				oracleStamp.ReplaceAllString(string(tj), "<t>"), texts, nudges}
		}
		label := fmt.Sprintf("seed %d step %d: PATCH %s %s caller %v enforced %v", seed, step, id, raw, caller, enforced)
		require.Equal(t, results[0], results[1], label)

		// Count what happened, from the outcome.
		r := results[1]
		var resp map[string]any
		_ = json.Unmarshal([]byte(r.body), &resp)
		msg, _ := resp["error"].(string)
		code, _ := resp["code"].(string)
		switch {
		case r.code == 200:
			counts["edited"]++
		case msg == "invalid task ID":
			counts["invalid task id"]++
		case msg == "invalid JSON":
			counts["invalid JSON"]++
		case code == "title_too_large":
			counts["title too large"]++
		case strings.Contains(msg, "/amend endpoint"):
			counts["body refused (amend)"]++
		case strings.Contains(msg, "use either remove_blocker"):
			counts["removing blockers mixed with other edits"]++
		case strings.Contains(msg, "may create or reassign shared tasks"):
			counts["not a planner (assign or remove blockers)"]++
		case msg == "Task not found":
			counts["title or priority of a missing task"]++
		case strings.Contains(msg, "change its title or priority"):
			counts["title or priority by a non-assignee"]++
		case msg == "subscriber_id required":
			counts["title or priority without an actor"]++
		case code == "assignee_archived":
			counts["archived assignee"]++
		case msg == "invalid blocked_by format":
			counts["bad blocked_by"]++
		case code == "identity_unverified":
			counts["identity unverified (enforce)"]++
		case r.code == 400:
			counts["the store refused"]++
		}
		if r.code != 200 {
			continue
		}
		var task board.Task
		require.NoError(t, json.Unmarshal([]byte(results[1].body), &task))
		var before *board.Task
		for i := range tasksBefore {
			if tasksBefore[i].ID == task.ID {
				before = &tasksBefore[i]
			}
		}
		require.NotNil(t, before, label)
		assignee := taskAssignee(&task)
		sess := memberSession(members, assignee)
		nudged := 0
		if sess != "" {
			nudged = claimNudges(sides[1].term, sess, task.ID) - nudgesBefore[sess]
		}
		newMessages := strings.Join(r.messages, "\n")
		switch {
		case before.Status == "pending" && task.Status == "blocked" && assignee != "":
			require.Contains(t, newMessages, fmt.Sprintf("[Task #%d blocked]", task.ID), label)
			counts["blocked, assignee told"]++
		case before.Status == "pending" && task.Status == "blocked":
			counts["blocked, no assignee"]++
		case before.Status == "blocked" && task.Status == "pending" && assignee != "":
			require.Contains(t, newMessages, "@"+assignee+" your task is now ready", label)
			if nudged > 0 {
				counts["unblocked, assignee told and nudged"]++
			}
		case before.Status == "blocked" && task.Status == "pending":
			counts["unblocked, no assignee"]++
		}
		_, reassigned := body["assigned_to"]
		if !reassigned || task.Status != "pending" || assignee == "" {
			continue
		}
		busy := false
		for _, other := range tasksBefore {
			if other.ID != task.ID && other.Status == "in_progress" && taskAssignee(&other) == assignee {
				busy = true
			}
		}
		switch {
		case before.Status == "blocked" && blocking:
			if nudged == 1 && assignee != taskAssignee(before) {
				counts["reassigned and unblocked, nudged once"]++
			}
		case assignee == taskAssignee(before):
			if nudged == 0 {
				counts["reassigned to the same assignee (no nudge)"]++
			}
		case busy:
			if nudged == 0 {
				counts["reassigned, new assignee busy (no nudge)"]++
			}
		case sess != "" && nudged == 1:
			counts["reassigned, new assignee nudged"]++
		}
	}
}

func memberSession(members []struct{ id, title, session string }, id string) string {
	for _, m := range members {
		if m.id == id {
			return m.session
		}
	}
	return ""
}

// claimNudges is how many claim nudges for the task a member's terminal has had.
func claimNudges(term *mockSessionTerminal, session string, taskID int64) int {
	term.mu.Lock()
	defer term.mu.Unlock()
	n := 0
	for _, line := range term.sent[session] {
		if strings.Contains(line, fmt.Sprintf("[Task #%d available]", taskID)) {
			n++
		}
	}
	return n
}

// updateTaskBefore4756 is UpdateTask as it was before #4756.
func (h *BoardHandler) updateTaskBefore4756(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	var raw struct {
		SubscriberID  string          `json:"subscriber_id"`
		Title         *string         `json:"title,omitempty"`
		Body          *string         `json:"body,omitempty"`
		Priority      *string         `json:"priority,omitempty"`
		AssignedTo    *string         `json:"assigned_to,omitempty"`
		BlockedBy     json.RawMessage `json:"blocked_by,omitempty"`
		RemoveBlocker *int64          `json:"remove_blocker,omitempty"`
		ClearBlockers bool            `json:"clear_blockers,omitempty"`
	}
	if err := decodeJSON(r, &raw); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	attrWho, attrOK := attributedTo(w, r, raw.SubscriberID)
	if !attrOK {
		return
	}
	raw.SubscriberID = attrWho
	if raw.Title != nil && refuseTextTooLarge(w, "title_too_large", "a task title", len(*raw.Title), maxTaskTitleBytes) {
		return
	}
	if raw.Body != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "task body changes require the revisioned /amend endpoint with a reason and base_revision"})
		return
	}

	removingBlockers := raw.RemoveBlocker != nil || raw.ClearBlockers
	if removingBlockers && (len(raw.BlockedBy) > 0 || raw.Title != nil || raw.Priority != nil || raw.AssignedTo != nil || (raw.RemoveBlocker != nil && (raw.ClearBlockers || *raw.RemoveBlocker <= 0))) {
		errBadRequest(w, "use either remove_blocker (positive task ID) or clear_blockers, without other edits")
		return
	}
	if (raw.AssignedTo != nil || removingBlockers) && !h.requireTaskPlanner(w, r, project, raw.SubscriberID) {
		return
	}
	// A task's title is typed into its assignee's terminal: only a planner or the assignee
	// changes what it says (#3668).
	if raw.Title != nil || raw.Priority != nil {
		current, err := h.bs.GetTask(r.Context(), project, taskID)
		if err != nil || current == nil {
			errNotFound(w, "Task not found")
			return
		}
		if !h.requirePlannerOrAssignee(w, r.Context(), project, current, raw.SubscriberID, "change its title or priority") {
			return
		}
	}
	if raw.AssignedTo != nil && h.refuseArchivedAssignee(w, r.Context(), project, *raw.AssignedTo) {
		return
	}
	update := board.TaskUpdate{
		Title:      raw.Title,
		Body:       raw.Body,
		Priority:   raw.Priority,
		AssignedTo: raw.AssignedTo,
	}

	if len(raw.BlockedBy) > 0 {
		deps, err := parseBlockedBy(raw.BlockedBy, project)
		if err != nil {
			errBadRequest(w, err.Error())
			return
		}
		update.BlockedBy = &deps
	}

	before, _ := h.bs.GetTask(r.Context(), project, taskID)
	var task *board.Task
	var prevStatus string
	if removingBlockers {
		var blocker int64
		if raw.RemoveBlocker != nil {
			blocker = *raw.RemoveBlocker
		}
		task, prevStatus, err = h.bs.RemoveTaskBlockers(r.Context(), project, taskID, blocker)
	} else {
		task, prevStatus, err = h.bs.UpdateTask(r.Context(), project, taskID, update, 32)
	}
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	_ = h.bs.TouchTask(r.Context(), project, taskID)
	h.goBG(func() {
		ctx := context.Background()
		nudged := false
		notification := fmt.Sprintf("[Task #%d edited] %s", task.ID, task.Title)
		h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", notification, nil)

		// Notify on status transitions from dependency changes
		if prevStatus == "pending" && task.Status == "blocked" {
			assignee := ""
			if task.AssignedTo != nil {
				assignee = *task.AssignedTo
			}
			if assignee != "" {
				msg := fmt.Sprintf("[Task #%d blocked] %s — @%s task is now blocked, please pause work", task.ID, task.Title, assignee)
				h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", msg, nil)
			}
		} else if prevStatus == "blocked" && task.Status == "pending" {
			assignee := ""
			if task.AssignedTo != nil {
				assignee = *task.AssignedTo
			}
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
		if raw.AssignedTo != nil && task.Status == "pending" {
			previous := ""
			if before != nil && before.AssignedTo != nil {
				previous = *before.AssignedTo
			}
			current := ""
			if task.AssignedTo != nil {
				current = *task.AssignedTo
			}
			if !nudged && current != "" && current != previous {
				if hasActive, _ := h.bs.HasActiveTaskForAssignee(ctx, project, current, task.ID); !hasActive {
					h.sendTaskNudge(ctx, project, current, taskClaimNudge(task.ID))
				}
			}
		}
	})
	writeJSON(w, http.StatusOK, task)
}
