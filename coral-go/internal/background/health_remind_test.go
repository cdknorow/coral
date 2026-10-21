package background

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
)

// task add --remind-at (#4175 step 3): at its time the assignee is reminded once, through the
// gate, with the task number and the time only; with nobody assigned, the orchestrator; a parked
// task waits until it is unparked; an agent that isn't running gets the board line only.
func TestTaskRemindersGoOutOnceAtTheirTime(t *testing.T) {
	s := healthMonitorStore(t)
	ctx := context.Background()
	var mu sync.Mutex
	sent := map[string][]string{}
	var agents []AgentInfo
	member := func(name, title string, n int, live bool) {
		sid := fmt.Sprintf("%08d-0000-0000-0000-000000000000", n)
		_, err := s.Subscribe(ctx, "team", name, title, naming.SessionName("claude", sid), nil, "all")
		require.NoError(t, err)
		if live {
			agents = append(agents, AgentInfo{AgentType: "claude", SessionID: sid, DisplayName: name})
		}
	}
	member("Orchestrator", "Orchestrator", 1, true)
	member("Dev", "Developer", 2, true)
	member("Away", "Developer", 3, false)
	now := time.Now()
	r := NewTaskReminders(s, nil, func(context.Context) ([]AgentInfo, error) { return agents, nil })
	r.now = func() time.Time { return now }
	r.deliver = func(_ context.Context, a AgentInfo, _ string, text string) error {
		mu.Lock()
		defer mu.Unlock()
		sent[a.DisplayName] = append(sent[a.DisplayName], text)
		return nil
	}
	task := func(title, assignee string, in time.Duration) int64 {
		var tk, err = s.CreateTask(ctx, "team", title, "", "medium", "Orchestrator", assignee)
		require.NoError(t, err)
		require.NoError(t, s.SetTaskRemindAt(ctx, "team", tk.ID, now.Add(in)))
		return tk.ID
	}
	mine := task("Secret soak title", "Dev", 30*time.Minute)
	orphan := task("unassigned", "", 30*time.Minute)
	parked := task("parked", "Dev", 30*time.Minute)
	away := task("away", "Away", 30*time.Minute)
	later := task("later", "Dev", 2*time.Hour)
	_, err := s.ParkTask(ctx, "team", parked, "waiting", time.Time{})
	require.NoError(t, err)

	r.RunOnce(ctx)
	assert.Empty(t, sent, "nothing is due yet")

	now = now.Add(31 * time.Minute)
	r.RunOnce(ctx)
	r.RunOnce(ctx)
	require.Len(t, sent["Dev"], 1, "once, and not the parked one nor the later one")
	assert.Contains(t, sent["Dev"][0], fmt.Sprintf("Reminder for #%d", mine))
	assert.NotContains(t, sent["Dev"][0], "Secret soak title", "no title")
	require.Len(t, sent["Orchestrator"], 1)
	assert.Contains(t, sent["Orchestrator"][0], fmt.Sprintf("#%d", orphan))
	msgs, err := s.ListMessages(ctx, "team", 50, 0, 0)
	require.NoError(t, err)
	var lines []string
	for _, m := range msgs {
		if strings.Contains(m.Content, "reminder]") {
			lines = append(lines, m.Content)
		}
	}
	all := strings.Join(lines, "\n")
	assert.Len(t, lines, 3, all)
	assert.Contains(t, all, fmt.Sprintf("[Task #%d reminder]", away))
	assert.Contains(t, all, "Away (not running: on the board only)")
	assert.NotContains(t, all, "Secret soak title")

	// Unparked after its time: reminded on the next pass.
	_, err = s.UnparkTask(ctx, "team", parked)
	require.NoError(t, err)
	r.RunOnce(ctx)
	require.Len(t, sent["Dev"], 2)
	assert.Contains(t, sent["Dev"][1], fmt.Sprintf("#%d", parked))
	now = now.Add(2 * time.Hour)
	r.RunOnce(ctx)
	assert.Contains(t, sent["Dev"][2], fmt.Sprintf("#%d", later))
}

// Security's MUST on #4225 (#4176 rule 5): ten reminders for one agent due together go out
// within the board's limits, at most 3 in an hour, the rest on later passes, none lost.
func TestTaskRemindersStayWithinTheLimits(t *testing.T) {
	s := healthMonitorStore(t)
	ctx := context.Background()
	sid := "00000002-0000-0000-0000-000000000000"
	_, err := s.Subscribe(ctx, "team", "Dev", "Developer", naming.SessionName("claude", sid), nil, "all")
	require.NoError(t, err)
	agents := []AgentInfo{{AgentType: "claude", SessionID: sid, DisplayName: "Dev"}}
	now := time.Now()
	var got []string
	r := NewTaskReminders(s, nil, func(context.Context) ([]AgentInfo, error) { return agents, nil })
	r.now = func() time.Time { return now }
	r.deliver = func(_ context.Context, _ AgentInfo, _ string, text string) error {
		got = append(got, text)
		return nil
	}
	ids := map[string]bool{}
	for i := 0; i < 10; i++ {
		tk, err := s.CreateTask(ctx, "team", fmt.Sprint("t", i), "", "medium", "Orchestrator", "Dev")
		require.NoError(t, err)
		require.NoError(t, s.SetTaskRemindAt(ctx, "team", tk.ID, now.Add(time.Minute)))
		ids[fmt.Sprintf("Reminder for #%d,", tk.ID)] = true
	}
	now = now.Add(2 * time.Minute)
	for i := 0; i < 10; i++ { // twenty passes in five minutes
		r.RunOnce(ctx)
		now = now.Add(30 * time.Second)
	}
	assert.Len(t, got, 3, "at most 3 in the hour")
	for hour := 0; hour < 4; hour++ {
		now = now.Add(61 * time.Minute)
		r.RunOnce(ctx)
	}
	assert.Len(t, got, 10, "the rest on later passes")
	for _, text := range got {
		delete(ids, text[strings.Index(text, "Reminder for #"):strings.Index(text, ",")+1])
	}
	assert.Empty(t, ids, "none lost, none twice")
}
