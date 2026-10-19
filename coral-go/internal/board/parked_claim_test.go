package board

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5183: a parked task is set aside on purpose, so it is never offered or claimed until it is unparked: not by a claim with no id (which takes the best pending task), not by
// naming it, and not as the "next task" an agent is pointed at; a reassign does not unpark it; an end that has passed makes it claimable again.
func TestParkedTaskIsNeverOfferedOrClaimed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	parked, err := s.CreateTask(ctx, "team", "Phase 2: wait for the operator", "", "critical", "lead", "")
	require.NoError(t, err)
	other, err := s.CreateTask(ctx, "team", "Ordinary work", "", "low", "lead", "")
	require.NoError(t, err)
	assigned, err := s.CreateTask(ctx, "team", "Assigned and parked", "", "critical", "lead", "Dev")
	require.NoError(t, err)
	_, err = s.ParkTask(ctx, "team", parked.ID, "waiting on phase 1", time.Time{})
	require.NoError(t, err)
	_, err = s.ParkTask(ctx, "team", assigned.ID, "waiting on the operator", time.Time{})
	require.NoError(t, err)

	// the next task and a claim with no id skip both parked tasks (they are the highest priority, so a miss would take them first)
	next := s.NextPendingTaskForSubscriber(ctx, "team", "Dev")
	require.NotNil(t, next)
	assert.Equal(t, other.ID, next.ID, "the next task is not a parked one")
	got, err := s.ClaimTask(ctx, "team", "Dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, other.ID, got.ID, "a claim with no id takes the ordinary task, not the parked unassigned or the parked assigned one")
	_, err = s.CompleteTask(ctx, "team", other.ID, "Dev", nil)
	require.NoError(t, err)
	none, err := s.ClaimTask(ctx, "team", "Dev")
	require.NoError(t, err)
	assert.Nil(t, none, "nothing is left to claim but parked tasks")

	// naming a parked task says why it cannot be claimed, for the unassigned one and for the assignee
	for _, id := range []int64{parked.ID, assigned.ID} {
		_, err = s.ClaimTask(ctx, "team", "Dev", id)
		assert.ErrorContains(t, err, "is parked and cannot be claimed until it is unparked")
	}

	// reassigning (even to nobody) leaves it parked, and still not claimable
	re, err := s.ReassignTask(ctx, "team", parked.ID, "")
	require.NoError(t, err)
	require.NotNil(t, re.ParkedReason, "a reassign does not unpark")
	none, err = s.ClaimTask(ctx, "team", "Dev")
	require.NoError(t, err)
	assert.Nil(t, none)

	// unparked, it is claimable again, highest priority first
	_, err = s.UnparkTask(ctx, "team", parked.ID)
	require.NoError(t, err)
	got, err = s.ClaimTask(ctx, "team", "Dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, parked.ID, got.ID)

	// an end in the past is not parked
	_, err = s.CompleteTask(ctx, "team", got.ID, "Dev", nil)
	require.NoError(t, err)
	_, err = s.ParkTask(ctx, "team", assigned.ID, "until a moment ago", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	got, err = s.ClaimTask(ctx, "team", "Dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, assigned.ID, got.ID, "its end has passed")
}
