package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/teamhealth"
)

// The health check end to end (#4246, before v1.4.74): one team on an isolated board, with
// mock agents (their state and the gate faked, everything else real: the board store, the
// routes, Coral's rules and the orchestrator's check), the team's thresholds shortened through
// the settings API, and a clock stepped five minutes at a time. At each step it asserts what the
// orchestrator and the others receive, and that no agent gets more than the board's limit of
// automatic actions in any hour.
func TestHealthCheckScenario(t *testing.T) {
	server, h := setupBoardTestServer(t)
	_ = server
	ctx := context.Background()
	const project = "team"

	// The clock Coral's rules and the routes' health actions read.
	clk := time.Now().UTC().Truncate(time.Second)
	now := func() time.Time { return clk }
	prevHealthNow := healthNow
	healthNow = now
	t.Cleanup(func() { healthNow = prevHealthNow })

	// The team: name, job title, and how it looks from its hook events.
	type member struct{ name, title, sid string }
	members := []member{
		{"Orchestrator", "Orchestrator", "00000000-4246-0000-0000-000000000001"},
		{"QA", "QA Engineer", "00000000-4246-0000-0000-000000000002"},
		{"Sec", "Security Reviewer", "00000000-4246-0000-0000-000000000003"},
		{"Author", "Developer", "00000000-4246-0000-0000-000000000004"},
		{"Dev", "Developer", "00000000-4246-0000-0000-000000000005"},
		{"Worker", "Developer", "00000000-4246-0000-0000-000000000006"},
		{"Asker", "Developer", "00000000-4246-0000-0000-000000000007"},
		{"Parker", "Developer", "00000000-4246-0000-0000-000000000008"},
		{"Spare", "Developer", "00000000-4246-0000-0000-000000000009"},
		{"Held", "Developer", "00000000-4246-0000-0000-000000000010"},
		{"Rem", "Developer", "00000000-4246-0000-0000-000000000011"},
	}
	sidOf := map[string]string{}
	var agents []background.AgentInfo
	for _, m := range members {
		_, err := h.bs.Subscribe(ctx, project, m.name, m.title, naming.SessionName("claude", m.sid), nil, "all")
		require.NoError(t, err)
		sidOf[m.name] = m.sid
		agents = append(agents, background.AgentInfo{AgentType: "claude", SessionID: m.sid, DisplayName: m.name})
	}
	var mu sync.Mutex
	signals := map[string]background.AgentSignal{}
	for _, m := range members { // busy unless a step says otherwise
		signals[m.sid] = background.AgentSignal{InTurn: true, Since: clk, LastEvent: clk}
	}
	signals[sidOf["Orchestrator"]] = background.AgentSignal{InTurn: true, Since: clk.Add(-time.Hour), LastEvent: clk.Add(-time.Hour)}
	signals[sidOf["Dev"]] = background.AgentSignal{Since: clk.Add(-15 * time.Minute), LastEvent: clk.Add(-15 * time.Minute)}
	signals[sidOf["Worker"]] = background.AgentSignal{Since: clk, LastEvent: clk}
	signals[sidOf["Parker"]] = background.AgentSignal{Since: clk.Add(-15 * time.Minute), LastEvent: clk.Add(-15 * time.Minute)}
	signals[sidOf["Spare"]] = background.AgentSignal{Since: clk.Add(-40 * time.Minute), LastEvent: clk.Add(-40 * time.Minute)}
	held := map[string]time.Time{naming.SessionName("claude", sidOf["Held"]): clk}

	// What each agent is typed (through the faked gate), with the time.
	type typed struct {
		at   time.Time
		text string
	}
	got := map[string][]typed{}
	hooks := background.HealthHooks{
		Now:      now,
		Discover: func(context.Context) ([]background.AgentInfo, error) { return agents, nil },
		Signal: func(_ context.Context, sid string) (background.AgentSignal, bool) {
			mu.Lock()
			defer mu.Unlock()
			s, ok := signals[sid]
			return s, ok
		},
		Deliver: func(_ context.Context, a background.AgentInfo, _ string, text string) error {
			mu.Lock()
			defer mu.Unlock()
			got[a.DisplayName] = append(got[a.DisplayName], typed{clk, text})
			return nil
		},
		HeldDialogs: func() map[string]time.Time { return held },
	}
	nudges := background.NewHealthNudges(h.bs, nil, nil, hooks.Discover)
	nudges.SetHooks(hooks)
	check := background.NewBoardHealthMonitor(h.bs, 5*time.Minute)
	check.SetHooks(hooks)
	reminders := background.NewTaskReminders(h.bs, nil, hooks.Discover)
	reminders.SetHooks(hooks)

	r := chi.NewRouter()
	r.Put("/api/board/{project}/settings", h.UpdateBoardSettings)
	r.Post("/api/board/{project}/tasks", h.CreateTask)
	r.Post("/api/board/{project}/tasks/{taskID}/submit-review", h.SubmitCompletionReview)
	r.Post("/api/board/{project}/waits", h.RegisterWait)
	r.Post("/api/board/{project}/tasks/{taskID}/park", h.ParkTask)
	r.Post("/api/board/{project}/health/ack", h.AckHealthItem)
	api := func(method, path string, body any, as string) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if as != "" { // an agent's own token
			req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{},
				&store.SessionTokenOwner{SessionID: sidOf[as], SessionName: naming.SessionName("claude", sidOf[as])}))
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// The Operator shortens the thresholds through the settings API (5 minutes is the floor).
	hs := board.DefaultHealthSettings()
	hs.On = true // off by default (#4962)
	hs.IntervalMin = 5
	hs.ClaimNudge = board.HealthClaimNudge{On: true, AfterMin: 5, Nudges: 2}
	hs.StatusNudge = board.HealthStatusNudge{On: true, AfterMin: 5, EscalateAfterMin: 5}
	hs.MentionRenudge = board.HealthMentionNudge{On: true, AfterMin: 5}
	hs.HeldNotice = board.HealthMentionNudge{On: true, AfterMin: 5}
	code, out := api(http.MethodPut, "/api/board/team/settings", map[string]any{"subscriber_id": "Operator", "health": hs}, "")
	require.Equal(t, http.StatusOK, code, "%v", out)
	code, _ = api(http.MethodPut, "/api/board/team/settings", map[string]any{"subscriber_id": "Operator", "health": hs}, "Orchestrator")
	require.Equal(t, http.StatusForbidden, code, "an agent token can't change them, the orchestrator's included")

	lines := func() []string {
		msgs, err := h.bs.ListMessages(ctx, project, 500, 0, 0)
		require.NoError(t, err)
		var out []string
		for _, m := range msgs {
			out = append(out, m.Content)
		}
		return out
	}
	linesWith := func(s string) []string {
		var out []string
		for _, l := range lines() {
			if strings.Contains(l, s) {
				out = append(out, l)
			}
		}
		return out
	}
	typedTo := func(name, with string) []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, x := range got[name] {
			if strings.Contains(x.text, with) {
				out = append(out, x.text)
			}
		}
		return out
	}
	lastCheck := func() string {
		checks := typedTo("Orchestrator", "[Coral health check]")
		require.NotEmpty(t, checks, "a check was typed to the orchestrator")
		return checks[len(checks)-1]
	}
	step := func() {
		clk = clk.Add(5*time.Minute + time.Second)
		nudges.RunOnce(ctx)
		reminders.RunOnce(ctx)
		check.Scan(ctx)
	}

	// 1. A task submitted for review with its reviewers named is delegated by Coral, with a board line.
	review, err := h.bs.CreateTaskWithOpts(ctx, project, "Review title kept out", "", "high", "Orchestrator",
		&board.CreateTaskOpts{Workflow: board.TaskWorkflow{Reviewers: []string{"QA", "Sec"}}}, "Author")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, project, "Author", review.ID)
	require.NoError(t, err)
	code, out = api(http.MethodPost, fmt.Sprintf("/api/board/team/tasks/%d/submit-review", review.ID),
		map[string]any{"subscriber_id": "Author", "reason": "ready", "message": "done", "artifacts": []board.TaskArtifact{{Name: "report", Content: "done"}}}, "Author")
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Eventually(t, func() bool { return len(linesWith("delegated for review by Coral")) == 1 }, 2*time.Second, 20*time.Millisecond)
	delegated := linesWith("delegated for review by Coral")[0]
	assert.Contains(t, delegated, fmt.Sprintf("[Task #%d delegated for review by Coral] to QA (#", review.ID))
	assert.Contains(t, delegated, "Sec (#")
	assert.NotContains(t, delegated, "Review title kept out")

	// The rest of the scene: Dev has assigned work it hasn't claimed, Worker a task in progress,
	// Asker mentions the orchestrator and asks the Operator a question, Parker's task is parked,
	// Author's next task has a reminder, Held's terminal shows a dialog.
	devTask, err := h.bs.CreateTask(ctx, project, "dev work", "", "high", "Orchestrator", "Dev")
	require.NoError(t, err)
	work, err := h.bs.CreateTask(ctx, project, "worker work", "", "high", "Orchestrator", "Worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, project, "Worker", work.ID)
	require.NoError(t, err)
	parked, err := h.bs.CreateTask(ctx, project, "parked work", "", "high", "Orchestrator", "Parker")
	require.NoError(t, err)
	code, _ = api(http.MethodPost, fmt.Sprintf("/api/board/team/tasks/%d/park", parked.ID), map[string]any{"reason": "waiting on the operator"}, "Parker")
	require.Equal(t, http.StatusForbidden, code, "an agent can't park its own task")
	code, out = api(http.MethodPost, fmt.Sprintf("/api/board/team/tasks/%d/park", parked.ID), map[string]any{"reason": "waiting on the operator"}, "Orchestrator")
	require.Equal(t, http.StatusOK, code, "%v", out)
	_, err = h.bs.PostMessage(ctx, project, "Asker", "@Orchestrator can you look at the gate?", nil)
	require.NoError(t, err)
	code, out = api(http.MethodPost, "/api/board/team/waits", map[string]any{"subscriber_id": "Asker", "wait_type": "operator", "reason": "ship v1.4.74 tonight?"}, "Asker")
	require.Equal(t, http.StatusCreated, code, "%v", out)
	remindAt := clk.Add(7 * time.Minute)
	code, out = api(http.MethodPost, "/api/board/team/tasks", map[string]any{"title": "soak ends", "subscriber_id": "Orchestrator",
		"assigned_to": "Author", "remind_at": remindAt.Format(time.RFC3339)}, "Orchestrator")
	require.Equal(t, http.StatusCreated, code, "%v", out)
	soakID := int64(out["id"].(float64))
	// Five tasks for Rem, all reminding at the same time (Security's note on #4246: the limits for
	// reminders, end to end).
	var remIDs []int64
	for i := 0; i < 5; i++ {
		code, out = api(http.MethodPost, "/api/board/team/tasks", map[string]any{"title": fmt.Sprint("gate step ", i), "subscriber_id": "Orchestrator",
			"assigned_to": "Rem", "remind_at": remindAt.Format(time.RFC3339)}, "Orchestrator")
		require.Equal(t, http.StatusCreated, code, "%v", out)
		remIDs = append(remIDs, int64(out["id"].(float64)))
	}

	// Step 1 (5 min): a claim nudge to Dev, a status nudge to Worker, the orchestrator reminded
	// of Asker's mention, the Operator told about Held's dialog; nothing for Parker; the check
	// lists Spare idle and Asker's question and counts the parked task.
	step()
	assert.Len(t, typedTo("Dev", fmt.Sprintf("coral-board task claim %d", devTask.ID)), 1, "claim nudge")
	assert.Len(t, typedTo("Worker", fmt.Sprintf("No activity on #%d", work.ID)), 1, "status nudge")
	assert.Len(t, typedTo("Orchestrator", "Asker mentioned you"), 1, "re-nudged once")
	assert.Empty(t, typedTo("Parker", ""), "nothing for a parked task")
	assert.Empty(t, typedTo("Held", ""), "nothing typed into a terminal at a dialog")
	heldLines := linesWith("waiting at a dialog")
	require.Len(t, heldLines, 1)
	assert.True(t, strings.HasPrefix(heldLines[0], "@Operator [Coral health] waiting at a dialog: Held ("), heldLines[0])
	c1 := lastCheck()
	assert.Contains(t, c1, "Spare: idle")
	assert.Contains(t, c1, "Waiting on the operator: Asker asked 5 min ago: quoted: ship v1.4.74 tonight?")
	assert.Contains(t, c1, "1 parked")
	assert.NotContains(t, c1, "Parker", "the parked task raises no item")

	// Step 2 (10 min): Dev's second claim nudge; Worker escalated; the reminder goes to Author;
	// the check names Worker. The orchestrator acknowledges Spare's item.
	step()
	assert.Len(t, typedTo("Dev", "coral-board task claim"), 2, "second claim nudge")
	assert.Len(t, typedTo("Worker", "No activity on"), 1, "one status nudge, then escalation")
	assert.Len(t, typedTo("Author", fmt.Sprintf("Reminder for #%d", soakID)), 1, "the reminder at its time")
	assert.Len(t, typedTo("Rem", "Reminder for #"), 3, "five due together: 3 in the hour")
	c2 := lastCheck()
	assert.Contains(t, c2, fmt.Sprintf("Worker: silent on #%d since Coral asked for a status", work.ID))
	m := regexp.MustCompile(`Spare: idle [^\[]*\[ack ([0-9a-f]{8})\]`).FindStringSubmatch(c2)
	require.Len(t, m, 2, c2)
	assert.Equal(t, teamhealth.ItemID("idle|Spare"), m[1])
	code, _ = api(http.MethodPost, "/api/board/team/health/ack", map[string]any{"item": m[1]}, "Spare")
	require.Equal(t, http.StatusForbidden, code, "an agent can't ack")
	code, out = api(http.MethodPost, "/api/board/team/health/ack", map[string]any{"item": m[1]}, "Orchestrator")
	require.Equal(t, http.StatusOK, code, "%v", out)

	// Step 3 (15 min): Dev escalated after two nudges; Spare's item stays off; no second
	// reminder of the mention, no second held notice.
	step()
	assert.Len(t, typedTo("Dev", "coral-board task claim"), 2, "no third nudge")
	c3 := lastCheck()
	assert.Contains(t, c3, fmt.Sprintf("Dev: has not claimed #%d after Coral's claim nudges", devTask.ID))
	assert.NotContains(t, c3, "Spare:", "acknowledged")
	assert.Len(t, typedTo("Orchestrator", "mentioned you"), 1)
	assert.Len(t, linesWith("waiting at a dialog"), 1)

	// Step 4 (20 min): Spare worked, then went idle again: the item is back, said once.
	mu.Lock()
	signals[sidOf["Spare"]] = background.AgentSignal{Since: clk.Add(-35 * time.Minute), LastEvent: clk.Add(-35 * time.Minute)}
	mu.Unlock()
	step()
	c4 := lastCheck()
	assert.Contains(t, c4, "Spare: idle")
	assert.Contains(t, c4, "(back: changed since it was acknowledged)")

	assert.Len(t, typedTo("Rem", "Reminder for #"), 3, "still 3 within the hour")

	// The hour is over: Rem's other two reminders go out, each of the five exactly once.
	clk = clk.Add(time.Hour)
	reminders.RunOnce(ctx)
	rem := typedTo("Rem", "Reminder for #")
	require.Len(t, rem, 5, "the rest on a later pass, none lost")
	for _, id := range remIDs {
		n := 0
		for _, text := range rem {
			if strings.Contains(text, fmt.Sprintf("Reminder for #%d,", id)) {
				n++
			}
		}
		assert.Equal(t, 1, n, "#%d reminded once", id)
	}

	// Every check fits its cap and carries no task title.
	for _, c := range typedTo("Orchestrator", "[Coral health check]") {
		assert.LessOrEqual(t, len(c), teamhealth.MaxCheck)
		for _, title := range []string{"Review title kept out", "dev work", "worker work", "parked work", "soak ends"} {
			assert.NotContains(t, c, title)
		}
	}

	// The limits (#4176 rule 5): no agent got more than 3 automatic actions in any hour (the
	// orchestrator's check is not one), and the board's lines from Coral's rules stay a few per step.
	mu.Lock()
	for name, list := range got {
		var at []time.Time
		for _, x := range list {
			if !strings.Contains(x.text, "[Coral health check]") {
				at = append(at, x.at)
			}
		}
		sort.Slice(at, func(i, j int) bool { return at[i].Before(at[j]) })
		for i := range at {
			n := 0
			for _, b := range at[i:] {
				if b.Sub(at[i]) < time.Hour {
					n++
				}
			}
			assert.LessOrEqual(t, n, 3, "%s: %d automatic actions within an hour", name, n)
		}
	}
	mu.Unlock()
	assert.LessOrEqual(t, len(linesWith("[Coral health")), 4*4, "at most one line per rule per step: %v", linesWith("[Coral health"))
}
