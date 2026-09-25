package board

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "board_test.db")
	s, err := NewStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// sub is a test helper that calls Subscribe with subscriberID used as both subscriber and session name.
func sub(s *Store, ctx context.Context, project, subscriberID, jobTitle string) (*Subscriber, error) {
	return s.Subscribe(ctx, project, subscriberID, jobTitle, "tmux-"+subscriberID, nil, nil, "")
}

func TestSubscribeAndList(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	result, err := sub(s, ctx, "myproject", "agent-1", "Lead Dev")
	require.NoError(t, err)
	assert.Equal(t, "Lead Dev", result.JobTitle)
	assert.Equal(t, "myproject", result.Project)

	subs, err := s.ListSubscribers(ctx, "myproject")
	require.NoError(t, err)
	assert.Len(t, subs, 1)

	// Upsert same subscriber updates job_title
	sub2, err := sub(s, ctx, "myproject", "agent-1", "Senior Dev")
	require.NoError(t, err)
	assert.Equal(t, "Senior Dev", sub2.JobTitle)

	subs, _ = s.ListSubscribers(ctx, "myproject")
	assert.Len(t, subs, 1) // Still just one

	// Unsubscribe
	removed, err := s.Unsubscribe(ctx, "myproject", "agent-1")
	require.NoError(t, err)
	assert.True(t, removed)

	removed, _ = s.Unsubscribe(ctx, "myproject", "agent-1")
	assert.False(t, removed) // Already inactive
}

func TestGetSubscription(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	result, err := s.GetSubscription(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, result)

	sub(s, ctx, "proj", "agent-1", "Dev")
	result, err = s.GetSubscription(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "proj", result.Project)
}

func TestPostAndReadMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "agent-1", "Dev A")
	sub(s, ctx, "proj", "agent-2", "Dev B")

	// Agent-1 posts
	msg, err := s.PostMessage(ctx, "proj", "agent-1", "Hello team!", nil)
	require.NoError(t, err)
	assert.Equal(t, "Hello team!", msg.Content)

	// Agent-2 reads — should see agent-1's message
	messages, err := s.ReadMessages(ctx, "proj", "agent-2", 50)
	require.NoError(t, err)
	assert.Len(t, messages, 1)
	assert.Equal(t, "Hello team!", messages[0].Content)
	assert.Equal(t, "Dev A", messages[0].JobTitle)

	// Agent-1 reads — should NOT see own message
	messages, err = s.ReadMessages(ctx, "proj", "agent-1", 50)
	require.NoError(t, err)
	assert.Empty(t, messages)

	// Agent-2 reads again — cursor advanced, nothing new
	messages, err = s.ReadMessages(ctx, "proj", "agent-2", 50)
	require.NoError(t, err)
	assert.Empty(t, messages)
}

func TestListMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "agent-1", "Dev")
	s.PostMessage(ctx, "proj", "agent-1", "msg 1", nil)
	s.PostMessage(ctx, "proj", "agent-1", "msg 2", nil)

	messages, err := s.ListMessages(ctx, "proj", 100, 0, 0)
	require.NoError(t, err)
	assert.Len(t, messages, 2)

	count, err := s.CountMessages(ctx, "proj")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestCheckUnread(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "agent-1", "Dev A")
	sub(s, ctx, "proj", "agent-2", "Dev B")

	// Post without mention — agent-2 should have 0 unread
	s.PostMessage(ctx, "proj", "agent-1", "Just a regular message", nil)
	count, err := s.CheckUnread(ctx, "proj", "agent-2")
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Post with @all mention
	s.PostMessage(ctx, "proj", "agent-1", "@notify-all Important update!", nil)
	count, err = s.CheckUnread(ctx, "proj", "agent-2")
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Post with @job_title mention
	s.PostMessage(ctx, "proj", "agent-1", "@Dev B please review this", nil)
	count, err = s.CheckUnread(ctx, "proj", "agent-2")
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Own messages don't count
	count, _ = s.CheckUnread(ctx, "proj", "agent-1")
	assert.Equal(t, 0, count)
}

func TestGetAllUnreadCounts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "agent-1", "Dev A")
	sub(s, ctx, "proj", "agent-2", "Dev B")

	s.PostMessage(ctx, "proj", "agent-1", "@all hello", nil)

	counts, err := s.GetAllUnreadCounts(ctx)
	require.NoError(t, err)
	// Counts are keyed by session_name (tmux-<subscriberID>)
	assert.Equal(t, 0, counts["tmux-agent-1"]) // Own message
	assert.Equal(t, 1, counts["tmux-agent-2"]) // Mentioned
}

func TestDeleteMessage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "agent-1", "Dev")
	msg, _ := s.PostMessage(ctx, "proj", "agent-1", "delete me", nil)

	removed, err := s.DeleteMessage(ctx, msg.ID)
	require.NoError(t, err)
	assert.True(t, removed)

	count, _ := s.CountMessages(ctx, "proj")
	assert.Equal(t, 0, count)
}

func TestListProjects(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sub(s, ctx, "proj-a", "agent-1", "Dev")
	sub(s, ctx, "proj-b", "agent-2", "Dev")
	s.PostMessage(ctx, "proj-a", "agent-1", "hello", nil)

	projects, err := s.ListProjects(ctx)
	require.NoError(t, err)
	assert.Len(t, projects, 2)
}

func TestGetSubscription_MultipleBoards(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Subscribe the same subscriber_id ("Orchestrator") to two different boards.
	// This simulates the stale subscription bug: same role on multiple teams.
	_, err := s.Subscribe(ctx, "board-A", "Orchestrator", "Orchestrator", "tmux-session-A", nil, nil, "")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "board-B", "Orchestrator", "Orchestrator", "tmux-session-B", nil, nil, "")
	require.NoError(t, err)

	// GetSubscription by subscriber_id returns *some* active subscription.
	// When timestamps collide (same second), which board is returned is not
	// guaranteed — this is exactly why GetSubscriptionBySessionName was added.
	result, err := s.GetSubscription(ctx, "Orchestrator")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "Orchestrator", result.SubscriberID)
	assert.Contains(t, []string{"board-A", "board-B"}, result.Project,
		"GetSubscription should return one of the active subscriptions")

	// GetSubscriptionBySessionName is the precise lookup — always returns
	// the exact board for the given tmux session.
	resultA, err := s.GetSubscriptionBySessionName(ctx, "tmux-session-A")
	require.NoError(t, err)
	require.NotNil(t, resultA)
	assert.Equal(t, "board-A", resultA.Project)
	assert.Equal(t, "Orchestrator", resultA.SubscriberID)

	resultB, err := s.GetSubscriptionBySessionName(ctx, "tmux-session-B")
	require.NoError(t, err)
	require.NotNil(t, resultB)
	assert.Equal(t, "board-B", resultB.Project)
	assert.Equal(t, "Orchestrator", resultB.SubscriberID)

	// GetSubscriptionBySessionName for nonexistent session returns nil
	resultNone, err := s.GetSubscriptionBySessionName(ctx, "tmux-nonexistent")
	require.NoError(t, err)
	assert.Nil(t, resultNone)
}

// ── Task Tests ──────────────────────────────────────────────────────

func TestCreateTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Basic task creation
	task, err := s.CreateTask(ctx, "proj", "Implement feature X", "", "high", "alice")
	require.NoError(t, err)
	assert.Equal(t, "Implement feature X", task.Title)
	assert.Equal(t, "high", task.Priority)
	assert.Equal(t, "pending", task.Status)
	assert.Equal(t, "alice", task.CreatedBy)
	assert.Nil(t, task.AssignedTo)

	// Task with body
	task2, err := s.CreateTask(ctx, "proj", "With body", "Detailed instructions here", "medium", "alice")
	require.NoError(t, err)
	assert.Equal(t, "With body", task2.Title)
	require.NotNil(t, task2.Body)
	assert.Equal(t, "Detailed instructions here", *task2.Body)

	// Task with empty body
	task3, err := s.CreateTask(ctx, "proj", "No body", "", "medium", "alice")
	require.NoError(t, err)
	assert.Equal(t, "No body", task3.Title)

	// Default priority
	task4, err := s.CreateTask(ctx, "proj", "Default prio", "", "", "alice")
	require.NoError(t, err)
	assert.Equal(t, "medium", task4.Priority)
}

func TestListTasks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.CreateTask(ctx, "proj", "Low task", "", "low", "alice")
	s.CreateTask(ctx, "proj", "Critical task", "", "critical", "alice")
	s.CreateTask(ctx, "proj", "High task", "", "high", "alice")

	// All tasks returned, sorted by priority
	tasks, err := s.ListTasks(ctx, "proj")
	require.NoError(t, err)
	assert.Len(t, tasks, 3)
	assert.Equal(t, "critical", tasks[0].Priority)
	assert.Equal(t, "high", tasks[1].Priority)
	assert.Equal(t, "low", tasks[2].Priority)

	// Different project returns nothing
	tasks, err = s.ListTasks(ctx, "other")
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestClaimTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.CreateTask(ctx, "proj", "Do work", "", "medium", "alice")

	// Claim succeeds — picks the only pending task
	claimed, err := s.ClaimTask(ctx, "proj", "bob")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, "in_progress", claimed.Status)
	require.NotNil(t, claimed.AssignedTo)
	assert.Equal(t, "bob", *claimed.AssignedTo)
	assert.NotNil(t, claimed.ClaimedAt)

	// No more tasks to claim
	task, err := s.ClaimTask(ctx, "proj", "charlie")
	require.NoError(t, err)
	assert.Nil(t, task)

	// Empty project returns nil
	task, err = s.ClaimTask(ctx, "empty", "bob")
	require.NoError(t, err)
	assert.Nil(t, task)
}

func TestClaimTask_PriorityOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.CreateTask(ctx, "proj", "Low task", "", "low", "alice")
	s.CreateTask(ctx, "proj", "Critical task", "", "critical", "alice")
	s.CreateTask(ctx, "proj", "High task", "", "high", "alice")

	// Should claim critical first
	claimed, err := s.ClaimTask(ctx, "proj", "bob")
	require.NoError(t, err)
	assert.Equal(t, "Critical task", claimed.Title)

	// Cannot claim another while first is in progress
	_, err = s.ClaimTask(ctx, "proj", "bob")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "complete your current task")

	// Complete it, then claim next
	s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)
	claimed, err = s.ClaimTask(ctx, "proj", "bob")
	require.NoError(t, err)
	assert.Equal(t, "High task", claimed.Title)

	// Complete and claim last
	s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)
	claimed, err = s.ClaimTask(ctx, "proj", "bob")
	require.NoError(t, err)
	assert.Equal(t, "Low task", claimed.Title)
}

func TestHasActiveTaskForAssignee(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task1, err := s.CreateTask(ctx, "proj", "Task 1", "", "medium", "alice", "bob")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "proj", "bob")
	require.NoError(t, err)

	task2, err := s.CreateTask(ctx, "proj", "Task 2", "", "medium", "alice", "bob")
	require.NoError(t, err)

	hasActive, err := s.HasActiveTaskForAssignee(ctx, "proj", "bob", task2.ID)
	require.NoError(t, err)
	assert.True(t, hasActive)

	hasActive, err = s.HasActiveTaskForAssignee(ctx, "proj", "proj-other", task2.ID)
	require.NoError(t, err)
	assert.False(t, hasActive)

	hasActive, err = s.HasActiveTaskForAssignee(ctx, "proj", "bob", task1.ID)
	require.NoError(t, err)
	assert.False(t, hasActive)
}

func TestCompleteTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.CreateTask(ctx, "proj", "Do work", "", "medium", "alice")
	claimed, _ := s.ClaimTask(ctx, "proj", "bob")

	msg := "All done"
	completed, err := s.CompleteTask(ctx, "proj", claimed.ID, "bob", &msg)
	require.NoError(t, err)
	assert.Equal(t, "completed", completed.Status)
	require.NotNil(t, completed.CompletedBy)
	assert.Equal(t, "bob", *completed.CompletedBy)
	require.NotNil(t, completed.CompletionMessage)
	assert.Equal(t, "All done", *completed.CompletionMessage)
	assert.NotNil(t, completed.CompletedAt)

	// Can't complete a task twice
	_, err = s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be completed")
}

func TestCompleteTask_PendingWithoutClaim(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// A task assigned to the operator is never claimed; they just finish it
	task, err := s.CreateTask(ctx, "proj", "Review the release", "", "medium", "orchestrator", "Operator")
	require.NoError(t, err)
	require.Equal(t, "pending", task.Status)

	completed, err := s.CompleteTask(ctx, "proj", task.ID, "Operator", nil)
	require.NoError(t, err)
	assert.Equal(t, "completed", completed.Status)
	require.NotNil(t, completed.CompletedBy)
	assert.Equal(t, "Operator", *completed.CompletedBy)
}

func TestTaskLifecycle_EndToEnd(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Create tasks with different priorities
	s.CreateTask(ctx, "proj", "Critical work", "", "critical", "alice")
	s.CreateTask(ctx, "proj", "Medium work", "", "medium", "alice")
	s.CreateTask(ctx, "proj", "Low work", "", "low", "alice")

	// All 3 present
	tasks, _ := s.ListTasks(ctx, "proj")
	assert.Len(t, tasks, 3)

	// Claim and complete all via ClaimTask (auto-selects by priority)
	for i := 0; i < 3; i++ {
		claimed, err := s.ClaimTask(ctx, "proj", "bob")
		require.NoError(t, err)
		require.NotNil(t, claimed)
		_, err = s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)
		require.NoError(t, err)
	}

	// Verify all completed
	tasks, _ = s.ListTasks(ctx, "proj")
	assert.Len(t, tasks, 3)
	for _, task := range tasks {
		assert.Equal(t, "completed", task.Status)
	}

	// No more to claim
	task, err := s.ClaimTask(ctx, "proj", "bob")
	require.NoError(t, err)
	assert.Nil(t, task)
}

func TestUpdateTask_PendingTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, err := s.CreateTask(ctx, "proj", "Original title", "Original body", "medium", "alice")
	require.NoError(t, err)

	// Update title only
	newTitle := "Updated title"
	updated, _, err := s.UpdateTask(ctx, "proj", task.ID, TaskUpdate{Title: &newTitle}, 3)
	require.NoError(t, err)
	assert.Equal(t, "Updated title", updated.Title)
	require.NotNil(t, updated.Body)
	assert.Equal(t, "Original body", *updated.Body)
	assert.Equal(t, "medium", updated.Priority)

	// Update multiple fields
	newBody := "New body"
	newPriority := "high"
	updated, _, err = s.UpdateTask(ctx, "proj", task.ID, TaskUpdate{Body: &newBody, Priority: &newPriority}, 3)
	require.NoError(t, err)
	assert.Equal(t, "Updated title", updated.Title)
	require.NotNil(t, updated.Body)
	assert.Equal(t, "New body", *updated.Body)
	assert.Equal(t, "high", updated.Priority)

	// No-op update returns task unchanged
	updated, _, err = s.UpdateTask(ctx, "proj", task.ID, TaskUpdate{}, 3)
	require.NoError(t, err)
	assert.Equal(t, "Updated title", updated.Title)
}

func TestUpdateTask_AssignedTo(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, err := s.CreateTask(ctx, "proj", "Task", "", "medium", "alice", "bob")
	require.NoError(t, err)

	// Reassign pending task — should stay pending
	newAssignee := "charlie"
	updated, _, err := s.UpdateTask(ctx, "proj", task.ID, TaskUpdate{AssignedTo: &newAssignee}, 3)
	require.NoError(t, err)
	assert.Equal(t, "pending", updated.Status)
	require.NotNil(t, updated.AssignedTo)
	assert.Equal(t, "charlie", *updated.AssignedTo)

	// Unassign via empty string
	empty := ""
	updated, _, err = s.UpdateTask(ctx, "proj", task.ID, TaskUpdate{AssignedTo: &empty}, 3)
	require.NoError(t, err)
	assert.Nil(t, updated.AssignedTo)
}

func TestUpdateTask_InProgressReassignResetsToPending(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.CreateTask(ctx, "proj", "Task", "", "medium", "alice", "bob")
	claimed, err := s.ClaimTask(ctx, "proj", "bob")
	require.NoError(t, err)
	assert.Equal(t, "in_progress", claimed.Status)

	// Reassigning an in_progress task should reset to pending
	newAssignee := "charlie"
	updated, _, err := s.UpdateTask(ctx, "proj", claimed.ID, TaskUpdate{AssignedTo: &newAssignee}, 3)
	require.NoError(t, err)
	assert.Equal(t, "pending", updated.Status)
	require.NotNil(t, updated.AssignedTo)
	assert.Equal(t, "charlie", *updated.AssignedTo)
	assert.Nil(t, updated.ClaimedAt)
}

func TestUpdateTask_CompletedTaskRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.CreateTask(ctx, "proj", "Task", "", "medium", "alice")
	claimed, _ := s.ClaimTask(ctx, "proj", "bob")
	s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)

	newTitle := "Should fail"
	_, _, err := s.UpdateTask(ctx, "proj", claimed.ID, TaskUpdate{Title: &newTitle}, 3)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be edited")
}

func TestUpdateTask_SkippedTaskRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, _ := s.CreateTask(ctx, "proj", "Task", "", "medium", "alice")
	s.CancelTask(ctx, "proj", task.ID, "alice", nil)

	newTitle := "Should fail"
	_, _, err := s.UpdateTask(ctx, "proj", task.ID, TaskUpdate{Title: &newTitle}, 3)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be edited")
}

func TestUpdateTask_EmptyTitleRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, err := s.CreateTask(ctx, "proj", "Has title", "", "medium", "alice")
	require.NoError(t, err)

	empty := ""
	_, _, err = s.UpdateTask(ctx, "proj", task.ID, TaskUpdate{Title: &empty}, 3)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "title cannot be empty")

	// Verify title unchanged
	updated, err := s.getTaskByID(ctx, "proj", task.ID)
	require.NoError(t, err)
	assert.Equal(t, "Has title", updated.Title)
}

func TestUpdateTask_InvalidPriorityRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, err := s.CreateTask(ctx, "proj", "Task", "", "medium", "alice")
	require.NoError(t, err)

	bad := "banana"
	_, _, err = s.UpdateTask(ctx, "proj", task.ID, TaskUpdate{Priority: &bad}, 3)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid priority")

	// Verify priority unchanged
	updated, err := s.getTaskByID(ctx, "proj", task.ID)
	require.NoError(t, err)
	assert.Equal(t, "medium", updated.Priority)
}

func TestUpdateTask_NonexistentTaskReturnsError(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	newTitle := "Nope"
	_, _, err := s.UpdateTask(ctx, "proj", 99999, TaskUpdate{Title: &newTitle}, 3)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ── Task Dependency Tests ──────────────────────────────────────────

func TestCreateTaskWithDeps_BlockedStatus(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, err := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	require.NoError(t, err)

	// Task B blocked by A — should start as blocked
	taskB, err := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})
	require.NoError(t, err)
	assert.Equal(t, "blocked", taskB.Status)

	// Verify dependencies stored
	deps, err := s.GetTaskDependencies(ctx, taskB.ID)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, taskA.ID, deps[0].TaskID)
}

func TestCreateTaskWithDeps_AlreadyResolved(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	claimed, _ := s.ClaimTask(ctx, "proj", "bob")
	s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)

	// Task B blocked by completed A — should start as pending
	taskB, err := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})
	require.NoError(t, err)
	assert.Equal(t, "pending", taskB.Status)
}

func TestCreateTaskWithDeps_MultipleBlockers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	taskB, _ := s.CreateTask(ctx, "proj", "Task B", "", "medium", "alice")

	// Task C blocked by both A and B
	taskC, err := s.CreateTaskWithOpts(ctx, "proj", "Task C", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{
			{TaskID: taskA.ID, BoardID: "proj"},
			{TaskID: taskB.ID, BoardID: "proj"},
		}, MaxDepth: 3})
	require.NoError(t, err)
	assert.Equal(t, "blocked", taskC.Status)

	deps, _ := s.GetTaskDependencies(ctx, taskC.ID)
	assert.Len(t, deps, 2)
}

func TestResolveDownstreamTasks_SingleBlocker(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	taskB, err := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})
	require.NoError(t, err)
	assert.Equal(t, "blocked", taskB.Status)

	// Complete A — B should unblock
	claimed, _ := s.ClaimTask(ctx, "proj", "bob")
	s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)

	unblocked, err := s.ResolveDownstreamTasks(ctx, "proj", taskA.ID)
	require.NoError(t, err)
	require.Len(t, unblocked, 1)
	assert.Equal(t, taskB.ID, unblocked[0].ID)
	assert.Equal(t, "pending", unblocked[0].Status)
}

func TestResolveDownstreamTasks_MultipleBlockers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	taskB, _ := s.CreateTask(ctx, "proj", "Task B", "", "medium", "alice")
	taskC, _ := s.CreateTaskWithOpts(ctx, "proj", "Task C", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{
			{TaskID: taskA.ID, BoardID: "proj"},
			{TaskID: taskB.ID, BoardID: "proj"},
		}, MaxDepth: 3})
	assert.Equal(t, "blocked", taskC.Status)

	// Complete A — C should still be blocked (B unresolved)
	claimed, _ := s.ClaimTask(ctx, "proj", "bob")
	assert.Equal(t, "Task A", claimed.Title)
	s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)

	unblocked, err := s.ResolveDownstreamTasks(ctx, "proj", taskA.ID)
	require.NoError(t, err)
	assert.Empty(t, unblocked)

	// Verify C is still blocked
	tasks, _ := s.ListTasks(ctx, "proj")
	for _, t2 := range tasks {
		if t2.ID == taskC.ID {
			assert.Equal(t, "blocked", t2.Status)
		}
	}

	// Complete B — now C should unblock
	claimed, _ = s.ClaimTask(ctx, "proj", "bob")
	assert.Equal(t, "Task B", claimed.Title)
	s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)

	unblocked, err = s.ResolveDownstreamTasks(ctx, "proj", taskB.ID)
	require.NoError(t, err)
	require.Len(t, unblocked, 1)
	assert.Equal(t, taskC.ID, unblocked[0].ID)
	assert.Equal(t, "pending", unblocked[0].Status)
}

func TestResolveDownstreamTasks_CancelledBlockerUnblocks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	taskB, _ := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})
	assert.Equal(t, "blocked", taskB.Status)

	// Cancel A — B should unblock (skipped counts as resolved)
	s.CancelTask(ctx, "proj", taskA.ID, "alice", nil)

	unblocked, err := s.ResolveDownstreamTasks(ctx, "proj", taskA.ID)
	require.NoError(t, err)
	require.Len(t, unblocked, 1)
	assert.Equal(t, taskB.ID, unblocked[0].ID)
	assert.Equal(t, "pending", unblocked[0].Status)
}

func TestReblockDownstreamTasks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// A is assigned to bob, B is blocked by A
	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice", "bob")
	taskB, _ := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})
	assert.Equal(t, "blocked", taskB.Status)

	// Claim A (in_progress), then reassign (resets to pending) → B stays blocked
	claimed, _ := s.ClaimTask(ctx, "proj", "bob")
	require.Equal(t, taskA.ID, claimed.ID)

	// Reassign in_progress task → resets to pending
	_, err := s.ReassignTask(ctx, "proj", taskA.ID, "charlie")
	require.NoError(t, err)

	// A is now pending again — B should remain blocked (A never resolved)
	reblocked, err := s.ReblockDownstreamTasks(ctx, "proj", taskA.ID)
	require.NoError(t, err)
	// B was already blocked so ReblockDownstreamTasks only transitions pending→blocked.
	// B never left blocked, so nothing to re-block.
	assert.Empty(t, reblocked)

	// Verify B is still blocked
	task, _ := s.getTaskByID(ctx, "proj", taskB.ID)
	assert.Equal(t, "blocked", task.Status)
}

func TestReblockDownstreamTasks_AfterUnblock(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// A and C are independent tasks. B is blocked by both A and C.
	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice", "bob")
	taskC, _ := s.CreateTask(ctx, "proj", "Task C", "", "medium", "alice", "charlie")
	taskB, _ := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{
			{TaskID: taskA.ID, BoardID: "proj"},
			{TaskID: taskC.ID, BoardID: "proj"},
		}, MaxDepth: 3})
	assert.Equal(t, "blocked", taskB.Status)

	// Complete both A and C → B unblocks
	claimed, _ := s.ClaimTask(ctx, "proj", "bob")
	s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)
	s.ResolveDownstreamTasks(ctx, "proj", taskA.ID)

	claimed, _ = s.ClaimTask(ctx, "proj", "charlie")
	s.CompleteTask(ctx, "proj", claimed.ID, "charlie", nil)
	unblocked, _ := s.ResolveDownstreamTasks(ctx, "proj", taskC.ID)
	require.Len(t, unblocked, 1)
	assert.Equal(t, "pending", unblocked[0].Status)

	// Now add a NEW unresolved dep to B via UpdateTask → B should go back to blocked
	newBlocker, _ := s.CreateTask(ctx, "proj", "Task D", "", "medium", "alice")
	deps := []TaskDep{{TaskID: newBlocker.ID, BoardID: "proj"}}
	updated, prevStatus, err := s.UpdateTask(ctx, "proj", taskB.ID, TaskUpdate{BlockedBy: &deps}, 3)
	require.NoError(t, err)
	assert.Equal(t, "pending", prevStatus)
	assert.Equal(t, "blocked", updated.Status)
}

func TestCircularDependencyRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	taskB, _ := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})
	require.NotNil(t, taskB)

	// Try to make A blocked by B (circular: A→B→A)
	err := s.AddTaskDependencies(ctx, "proj", taskA.ID, []TaskDep{{TaskID: taskB.ID, BoardID: "proj"}}, 3)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

func TestSelfDependencyRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, _ := s.CreateTask(ctx, "proj", "Task", "", "medium", "alice")

	err := s.AddTaskDependencies(ctx, "proj", task.ID, []TaskDep{{TaskID: task.ID, BoardID: "proj"}}, 3)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot depend on itself")
}

func TestDepthLimitEnforced(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Create chain: A → B → C → D (depth 3)
	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	taskB, _ := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})
	require.NotNil(t, taskB)
	taskC, _ := s.CreateTaskWithOpts(ctx, "proj", "Task C", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskB.ID, BoardID: "proj"}}, MaxDepth: 3})
	require.NotNil(t, taskC)

	// Depth 3 chain: D → C → B → A — should succeed at maxDepth=3
	taskD, err := s.CreateTaskWithOpts(ctx, "proj", "Task D", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskC.ID, BoardID: "proj"}}, MaxDepth: 3})
	require.NoError(t, err)
	require.NotNil(t, taskD)

	// Depth 4 chain: E → D → C → B → A — should fail at maxDepth=3
	_, err = s.CreateTaskWithOpts(ctx, "proj", "Task E", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskD.ID, BoardID: "proj"}}, MaxDepth: 3})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceed maximum depth")
}

func TestBlockedTaskCannotBeClaimed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	s.CreateTaskWithOpts(ctx, "proj", "Task B (blocked)", "", "high", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})

	// Claim should pick A (pending), not B (blocked, even though higher priority)
	claimed, err := s.ClaimTask(ctx, "proj", "bob")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, "Task A", claimed.Title)
}

func TestBlockedTaskCanBeCancelled(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	taskB, _ := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})
	assert.Equal(t, "blocked", taskB.Status)

	// Cancel blocked task should work
	cancelled, err := s.CancelTask(ctx, "proj", taskB.ID, "alice", nil)
	require.NoError(t, err)
	assert.Equal(t, "skipped", cancelled.Status)
}

func TestUpdateTask_AddBlockedBy(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	taskB, _ := s.CreateTask(ctx, "proj", "Task B", "", "medium", "alice")
	assert.Equal(t, "pending", taskB.Status)

	// Add dependency: B blocked by A → should transition to blocked
	deps := []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}
	updated, prevStatus, err := s.UpdateTask(ctx, "proj", taskB.ID, TaskUpdate{BlockedBy: &deps}, 3)
	require.NoError(t, err)
	assert.Equal(t, "pending", prevStatus)
	assert.Equal(t, "blocked", updated.Status)
}

func TestUpdateTask_ClearBlockedBy(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	taskB, _ := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})
	assert.Equal(t, "blocked", taskB.Status)

	// Clear dependencies → should unblock
	emptyDeps := []TaskDep{}
	updated, prevStatus, err := s.UpdateTask(ctx, "proj", taskB.ID, TaskUpdate{BlockedBy: &emptyDeps}, 3)
	require.NoError(t, err)
	assert.Equal(t, "blocked", prevStatus)
	assert.Equal(t, "pending", updated.Status)

	// Verify no deps remain
	deps, _ := s.GetTaskDependencies(ctx, taskB.ID)
	assert.Empty(t, deps)
}

func TestListTasks_PopulatesDeps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	taskA, _ := s.CreateTask(ctx, "proj", "Task A", "", "medium", "alice")
	s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: taskA.ID, BoardID: "proj"}}, MaxDepth: 3})

	tasks, err := s.ListTasks(ctx, "proj")
	require.NoError(t, err)

	var foundB *Task
	for i := range tasks {
		if tasks[i].Title == "Task B" {
			foundB = &tasks[i]
			break
		}
	}
	require.NotNil(t, foundB)
	require.Len(t, foundB.BlockedBy, 1)
	assert.Equal(t, taskA.ID, foundB.BlockedBy[0].TaskID)
	assert.Equal(t, "Task A", foundB.BlockedBy[0].Title)
}

func TestNonexistentBlockerRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, err := s.CreateTaskWithOpts(ctx, "proj", "Task B", "", "medium", "alice",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: 99999, BoardID: "proj"}}, MaxDepth: 3})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ── Draft Task Tests ──────────────────────────────────────────────

func TestCreateDraftTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, err := s.CreateTaskWithOpts(ctx, "proj", "Draft task", "body", "medium", "alice",
		&CreateTaskOpts{Draft: true})
	require.NoError(t, err)
	assert.Equal(t, "draft", task.Status)
}

func TestDraftTaskCannotBeClaimed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.CreateTaskWithOpts(ctx, "proj", "Draft", "", "high", "alice",
		&CreateTaskOpts{Draft: true})
	s.CreateTask(ctx, "proj", "Pending", "", "low", "alice")

	// Claim should pick the pending task, not the draft
	claimed, err := s.ClaimTask(ctx, "proj", "bob")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, "Pending", claimed.Title)
}

func TestPublishDraft_NoDeps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, _ := s.CreateTaskWithOpts(ctx, "proj", "Draft", "", "medium", "alice",
		&CreateTaskOpts{Draft: true})
	assert.Equal(t, "draft", task.Status)

	published, err := s.PublishTask(ctx, "proj", task.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", published.Status)
}

func TestPublishDraft_WithUnresolvedDeps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	blocker, _ := s.CreateTask(ctx, "proj", "Blocker", "", "medium", "alice")
	draft, _ := s.CreateTaskWithOpts(ctx, "proj", "Draft with deps", "", "medium", "alice",
		&CreateTaskOpts{Draft: true, BlockedBy: []TaskDep{{TaskID: blocker.ID, BoardID: "proj"}}, MaxDepth: 3})
	assert.Equal(t, "draft", draft.Status)

	published, err := s.PublishTask(ctx, "proj", draft.ID)
	require.NoError(t, err)
	assert.Equal(t, "blocked", published.Status)
}

func TestPublishDraft_WithResolvedDeps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	blocker, _ := s.CreateTask(ctx, "proj", "Blocker", "", "medium", "alice")
	claimed, _ := s.ClaimTask(ctx, "proj", "bob")
	s.CompleteTask(ctx, "proj", claimed.ID, "bob", nil)

	draft, _ := s.CreateTaskWithOpts(ctx, "proj", "Draft after blocker", "", "medium", "alice",
		&CreateTaskOpts{Draft: true, BlockedBy: []TaskDep{{TaskID: blocker.ID, BoardID: "proj"}}, MaxDepth: 3})
	assert.Equal(t, "draft", draft.Status)

	published, err := s.PublishTask(ctx, "proj", draft.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", published.Status)
}

func TestPublishNonDraft_Fails(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, _ := s.CreateTask(ctx, "proj", "Pending task", "", "medium", "alice")
	assert.Equal(t, "pending", task.Status)

	_, err := s.PublishTask(ctx, "proj", task.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a draft")
}

func TestDraftTaskEditable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, _ := s.CreateTaskWithOpts(ctx, "proj", "Draft", "body", "medium", "alice",
		&CreateTaskOpts{Draft: true})

	newTitle := "Updated draft"
	updated, _, err := s.UpdateTask(ctx, "proj", task.ID, TaskUpdate{Title: &newTitle}, 3)
	require.NoError(t, err)
	assert.Equal(t, "Updated draft", updated.Title)
	assert.Equal(t, "draft", updated.Status)
}

func TestDraftTaskCanBeCancelled(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, _ := s.CreateTaskWithOpts(ctx, "proj", "Draft to cancel", "", "medium", "alice",
		&CreateTaskOpts{Draft: true})

	cancelled, err := s.CancelTask(ctx, "proj", task.ID, "alice", nil)
	require.NoError(t, err)
	assert.Equal(t, "skipped", cancelled.Status)
}

func TestDraftTaskCannotBeCompleted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, _ := s.CreateTaskWithOpts(ctx, "proj", "Draft", "", "medium", "alice",
		&CreateTaskOpts{Draft: true})

	_, err := s.CompleteTask(ctx, "proj", task.ID, "alice", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be completed")
}

func TestDeleteProject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "agent-1", "Dev")
	s.PostMessage(ctx, "proj", "agent-1", "hello", nil)

	err := s.DeleteProject(ctx, "proj")
	require.NoError(t, err)

	subs, _ := s.ListSubscribers(ctx, "proj")
	assert.Empty(t, subs)
	count, _ := s.CountMessages(ctx, "proj")
	assert.Equal(t, 0, count)
}

func TestMarkReadThrough(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch", nil, nil, "all")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "proj", "Dev", "Dev", "claude-dev", nil, nil, "all")
	require.NoError(t, err)

	var ids []int64
	for i := 0; i < 10; i++ {
		m, err := s.PostMessage(ctx, "proj", "Dev", fmt.Sprintf("update %d", i), nil)
		require.NoError(t, err)
		ids = append(ids, m.ID)
	}
	own, err := s.PostMessage(ctx, "proj", "Orchestrator", "my own post", nil)
	require.NoError(t, err)
	unread, _ := s.CheckUnread(ctx, "proj", "Orchestrator")
	require.Equal(t, 10, unread)

	// Looked at the newest three (#8, #9 and its own post): the first seven were skipped
	skipped, first, last, err := s.MarkReadThrough(ctx, "proj", "Orchestrator", own.ID, ids[7])
	require.NoError(t, err)
	assert.Equal(t, 7, skipped)
	assert.Equal(t, ids[0], first)
	assert.Equal(t, ids[6], last)
	unread, _ = s.CheckUnread(ctx, "proj", "Orchestrator")
	assert.Equal(t, 0, unread)

	// Never moves back
	skipped, _, _, err = s.MarkReadThrough(ctx, "proj", "Orchestrator", ids[2], ids[2])
	require.NoError(t, err)
	assert.Equal(t, 0, skipped)
	unread, _ = s.CheckUnread(ctx, "proj", "Orchestrator")
	assert.Equal(t, 0, unread)

	_, _, _, err = s.MarkReadThrough(ctx, "proj", "Nobody", own.ID, own.ID)
	assert.Error(t, err)
}
