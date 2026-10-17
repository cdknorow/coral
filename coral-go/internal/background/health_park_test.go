package background

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4175 step 3: a parked task gets no claim nudge and no status nudge while it is parked; its
// end passing brings them back.
func TestParkedTasksAreNotNudged(t *testing.T) {
	n := newNudgeHarness(t, "")
	ctx := context.Background()
	idle, working := "77777777-7777-7777-7777-777777777777", "88888888-8888-8888-8888-888888888888"
	n.agent("Idle", idle)
	n.agent("Worker", working)
	queued, err := n.bs.CreateTask(ctx, "team", "queued", "", "high", "Operator", "Idle")
	require.NoError(t, err)
	busy, err := n.bs.CreateTask(ctx, "team", "busy", "", "high", "Operator", "Worker")
	require.NoError(t, err)
	_, err = n.bs.ClaimTask(ctx, "team", "Worker", busy.ID)
	require.NoError(t, err)
	n.sig[idle] = agentSignal{Since: n.now.Add(-time.Hour)}
	n.sig[working] = agentSignal{Since: n.now.Add(-time.Hour), LastEvent: n.now.Add(-time.Hour)}

	_, err = n.bs.ParkTask(ctx, "team", queued.ID, "waiting on the operator", time.Time{})
	require.NoError(t, err)
	_, err = n.bs.ParkTask(ctx, "team", busy.ID, "soak running", time.Now().Add(time.Hour))
	require.NoError(t, err)
	n.run(0)
	n.run(30 * time.Minute)
	assert.Empty(t, n.sentTo("Idle"), "no claim nudge for a parked task")
	assert.Empty(t, n.sentTo("Worker"), "no status nudge for a parked task")

	// Unparked (or its end passed, which Parked reads the same way): the nudges come back.
	_, err = n.bs.UnparkTask(ctx, "team", queued.ID)
	require.NoError(t, err)
	_, err = n.bs.ParkTask(ctx, "team", busy.ID, "soak running", time.Now().Add(time.Second))
	require.NoError(t, err)
	n.now = time.Now().Add(time.Hour)
	n.run(0)
	assert.Len(t, n.sentTo("Idle"), 1)
	assert.Len(t, n.sentTo("Worker"), 1)
}

// A parked task raises no item in the orchestrator's check: not an escalation, not a review in
// a reviewer's queue.
func TestParkedTasksRaiseNoCheckItem(t *testing.T) {
	h := newCheckHarness(t)
	ctx := context.Background()
	h.member("Orchestrator", "Orchestrator", 1, 2*time.Hour)
	h.member("Dev", "Developer", 2, 0)
	work, err := h.s.CreateTask(ctx, "team", "work", "", "high", "Operator", "Dev")
	require.NoError(t, err)
	_, err = h.s.ClaimTask(ctx, "team", "Dev", work.ID)
	require.NoError(t, err)
	require.NoError(t, h.s.MarkHealthEscalated(ctx, "team", ruleStatusNudge, "Dev", fmt.Sprint(work.ID), h.now.Add(-5*time.Minute)))
	_, err = h.s.ParkTask(ctx, "team", work.ID, "waiting on the operator", time.Time{})
	require.NoError(t, err)
	out := h.checkTo("Orchestrator")
	assert.NotContains(t, out, fmt.Sprintf("#%d", work.ID), out)
	assert.Contains(t, out, "Nothing needs you")
}
