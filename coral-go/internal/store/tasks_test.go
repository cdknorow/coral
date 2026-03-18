package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentTasksCRUD(t *testing.T) {
	db := openTestDB(t)
	s := NewTaskStore(db)
	ctx := context.Background()

	// Create tasks
	task1, err := s.CreateAgentTask(ctx, "agent-1", "Fix bug", nil)
	require.NoError(t, err)
	assert.Equal(t, "Fix bug", task1.Title)
	assert.Equal(t, 0, task1.SortOrder)

	task2, err := s.CreateAgentTask(ctx, "agent-1", "Add tests", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, task2.SortOrder) // auto-increment

	// List
	tasks, err := s.ListAgentTasks(ctx, "agent-1", nil)
	require.NoError(t, err)
	assert.Len(t, tasks, 2)

	// Update
	completed := 1
	err = s.UpdateAgentTask(ctx, task1.ID, nil, &completed, nil)
	require.NoError(t, err)

	// Complete by title
	err = s.CompleteAgentTaskByTitle(ctx, "agent-1", "Add tests", nil)
	require.NoError(t, err)

	// Create if not exists (should return existing)
	existing, err := s.CreateAgentTaskIfNotExists(ctx, "agent-1", "Fix bug", nil)
	require.NoError(t, err)
	assert.Equal(t, task1.ID, existing.ID)

	// Reorder
	err = s.ReorderAgentTasks(ctx, "agent-1", []int64{task2.ID, task1.ID})
	require.NoError(t, err)
	tasks, _ = s.ListAgentTasks(ctx, "agent-1", nil)
	assert.Equal(t, task2.ID, tasks[0].ID)

	// Delete
	err = s.DeleteAgentTask(ctx, task1.ID)
	require.NoError(t, err)
	tasks, _ = s.ListAgentTasks(ctx, "agent-1", nil)
	assert.Len(t, tasks, 1)
}

func TestAgentTasksWithSession(t *testing.T) {
	db := openTestDB(t)
	s := NewTaskStore(db)
	ctx := context.Background()

	sid := "sess-abc"
	task, err := s.CreateAgentTask(ctx, "agent-1", "Session task", &sid)
	require.NoError(t, err)

	// List by session
	tasks, err := s.ListAgentTasks(ctx, "agent-1", &sid)
	require.NoError(t, err)
	assert.Len(t, tasks, 1)
	assert.Equal(t, task.ID, tasks[0].ID)

	// List by session (history query)
	bySession, err := s.ListTasksBySession(ctx, "sess-abc")
	require.NoError(t, err)
	assert.Len(t, bySession, 1)
}

func TestAgentNotesCRUD(t *testing.T) {
	db := openTestDB(t)
	s := NewTaskStore(db)
	ctx := context.Background()

	note, err := s.CreateAgentNote(ctx, "agent-1", "Remember this", nil)
	require.NoError(t, err)
	assert.Equal(t, "Remember this", note.Content)

	notes, err := s.ListAgentNotes(ctx, "agent-1", nil)
	require.NoError(t, err)
	assert.Len(t, notes, 1)

	err = s.UpdateAgentNote(ctx, note.ID, "Updated content")
	require.NoError(t, err)

	err = s.DeleteAgentNote(ctx, note.ID)
	require.NoError(t, err)
	notes, _ = s.ListAgentNotes(ctx, "agent-1", nil)
	assert.Empty(t, notes)
}

func TestAgentEventsCRUD(t *testing.T) {
	db := openTestDB(t)
	s := NewTaskStore(db)
	ctx := context.Background()

	sid := "sess-1"
	toolName := "Edit"

	event, err := s.InsertAgentEvent(ctx, &AgentEvent{
		AgentName: "agent-1",
		SessionID: &sid,
		EventType: "tool_use",
		ToolName:  &toolName,
		Summary:   "Edited main.go",
	})
	require.NoError(t, err)
	assert.Equal(t, "tool_use", event.EventType)

	// List
	events, err := s.ListAgentEvents(ctx, "agent-1", 50, &sid)
	require.NoError(t, err)
	assert.Len(t, events, 1)

	// Tool counts
	counts, err := s.GetAgentEventCounts(ctx, "agent-1", &sid)
	require.NoError(t, err)
	assert.Len(t, counts, 1)
	assert.Equal(t, "Edit", counts[0].ToolName)
	assert.Equal(t, 1, counts[0].Count)

	// Goal event
	s.InsertAgentEvent(ctx, &AgentEvent{
		AgentName: "agent-1", SessionID: &sid,
		EventType: "goal", Summary: "Build auth system",
	})
	goals, err := s.GetLatestGoals(ctx, []string{"sess-1"})
	require.NoError(t, err)
	assert.Equal(t, "Build auth system", goals["sess-1"])

	// Latest event types (excludes status/goal/confidence)
	latest, err := s.GetLatestEventTypes(ctx, []string{"sess-1"})
	require.NoError(t, err)
	assert.Equal(t, "tool_use", latest["sess-1"][0])

	// Clear
	err = s.ClearAgentEvents(ctx, "agent-1", &sid)
	require.NoError(t, err)
	events, _ = s.ListAgentEvents(ctx, "agent-1", 50, &sid)
	assert.Empty(t, events)
}

func TestLastKnownStatusSummary(t *testing.T) {
	db := openTestDB(t)
	s := NewTaskStore(db)
	ctx := context.Background()

	sid := "sess-1"
	s.InsertAgentEvent(ctx, &AgentEvent{
		AgentName: "agent-1", SessionID: &sid,
		EventType: "status", Summary: "Working on feature",
	})
	s.InsertAgentEvent(ctx, &AgentEvent{
		AgentName: "agent-1", SessionID: &sid,
		EventType: "goal", Summary: "Implement auth",
	})

	result, err := s.GetLastKnownStatusSummary(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result["sess-1"]["status"])
	assert.Equal(t, "Working on feature", *result["sess-1"]["status"])
	assert.Equal(t, "Implement auth", *result["sess-1"]["summary"])
}
