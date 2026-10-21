package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/store"
)

// The pieces of a board's status (#4599: split out of teamAvailabilityUncached, behaviour unchanged).

func TestTaskStateOf(t *testing.T) {
	board1 := func(id int64, status string) availabilityTask { return availabilityTask{id, "board", "t", status} }
	personal1 := func(id int64, status string) availabilityTask { return availabilityTask{id, "personal", "t", status} }
	for _, c := range []struct {
		name        string
		tasks       []availabilityTask
		sub         string
		personal    []store.AgentTask
		underReview map[int64]string
		want        string
	}{
		{"nothing", nil, "dev", nil, nil, ""},
		{"in progress", []availabilityTask{board1(1, "in_progress")}, "dev", nil, nil, "active"},
		{"personal in progress", []availabilityTask{personal1(1, "in_progress")}, "", nil, nil, "active"},
		{"blocked", []availabilityTask{board1(1, "blocked")}, "dev", nil, nil, "blocked"},
		{"in progress wins over blocked, in either order", []availabilityTask{board1(1, "blocked"), board1(2, "in_progress")}, "dev", nil, nil, "active"},
		{"a board task under review is not active", []availabilityTask{board1(1, "in_progress")}, "dev", nil, map[int64]string{1: "dev"}, "awaiting-review"},
		{"a personal task with the same id under review still is", []availabilityTask{personal1(1, "in_progress")}, "dev", nil, map[int64]string{1: "dev"}, "active"},
		{"a personal task pending review", []availabilityTask{board1(1, "pending")}, "", []store.AgentTask{{Status: "review_pending"}}, nil, "awaiting-review"},
		{"another subscriber's review is not this one's", []availabilityTask{board1(1, "pending")}, "dev", nil, map[int64]string{9: "qa"}, ""},
		{"no subscriber: a board review is nobody's", nil, "", nil, map[int64]string{9: ""}, ""},
		{"blocked beats awaiting review", []availabilityTask{board1(1, "blocked")}, "dev", nil, map[int64]string{9: "dev"}, "blocked"},
	} {
		assert.Equal(t, c.want, taskStateOf(c.tasks, c.sub, c.personal, c.underReview), c.name)
	}
}

func TestBoardTasksAssignedTo(t *testing.T) {
	s := func(v string) *string { return &v }
	tasks := []board.Task{
		{ID: 1, Title: "a", Status: "pending", AssignedTo: s("dev")},
		{ID: 2, Title: "b", Status: "completed", AssignedTo: s("dev")},
		{ID: 3, Title: "c", Status: "in_progress"},
		{ID: 4, Title: "d", Status: "draft", AssignedTo: s("")},
		{ID: 5, Title: "e", Status: "blocked", AssignedTo: s("qa")},
		{ID: 6, Title: "f", Status: "skipped"},
	}
	assert.Equal(t, []availabilityTask{{1, "board", "a", "pending"}}, boardTasksAssignedTo(tasks, "dev"), "open ones only")
	assert.Equal(t, []availabilityTask{{3, "board", "c", "in_progress"}, {4, "board", "d", "draft"}}, boardTasksAssignedTo(tasks, ""), "unassigned: no assignee or an empty one")
	assert.Equal(t, []availabilityTask{}, boardTasksAssignedTo(tasks, "nobody"), "an empty list, not nil (the JSON says [])")
}

func TestActivityAuthorized(t *testing.T) {
	subs := []board.Subscriber{
		{SubscriberID: "peeker", IsActive: 1, CanPeek: 1},
		{SubscriberID: "orch", IsActive: 1, JobTitle: " orchestrator "},
		{SubscriberID: "operator", IsActive: 1},
		{SubscriberID: "dev", IsActive: 1, JobTitle: "Developer"},
		{SubscriberID: "gone", IsActive: 0, CanPeek: 1},
	}
	for caller, want := range map[string]bool{"peeker": true, "orch": true, "operator": true, "dev": false, "gone": false, "": false, "stranger": false} {
		assert.Equal(t, want, activityAuthorized(subs, caller), caller)
	}
}

func TestAvailabilitySummary(t *testing.T) {
	got := availabilitySummary([]availableAgent{{Availability: "available"}, {Availability: "busy"}, {Availability: "busy"}, {Availability: "offline"}})
	assert.Equal(t, 4, got["total"])
	assert.Equal(t, 1, got["available"])
	assert.Equal(t, 2, got["busy"])
	assert.Equal(t, 1, got["offline"])
	assert.Equal(t, 0, got["waiting"], "every state is listed, at 0 when none")
	assert.Len(t, got, 12)
}

func TestWaitingReason(t *testing.T) {
	assert.Equal(t, "Waiting for task #12 (a review)", waitingReason(&board.RegisteredWait{WaitType: "task", TargetID: "12", Reason: "a review"}))
	assert.Equal(t, "Waiting for task #12", waitingReason(&board.RegisteredWait{WaitType: "task", TargetID: "12"}))
}
