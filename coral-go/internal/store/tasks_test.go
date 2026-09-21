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
	task1, err := s.CreateAgentTask(ctx, "agent-1", "Fix bug", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "Fix bug", task1.Title)
	assert.Equal(t, 0, task1.SortOrder)

	task2, err := s.CreateAgentTask(ctx, "agent-1", "Add tests", nil, nil)
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
	task, err := s.CreateAgentTask(ctx, "agent-1", "Session task", &sid, nil)
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

func TestAgentEventsRetentionIsolatedPerSession(t *testing.T) {
	db := openTestDB(t)
	s := NewTaskStore(db)
	ctx := context.Background()
	sessionA, sessionB := "session-a", "session-b"
	_, err := s.InsertAgentEvent(ctx, &AgentEvent{AgentName: "shared-agent", SessionID: &sessionA, EventType: "notification", Summary: "Claude needs your permission"})
	require.NoError(t, err)
	for i := 0; i < 501; i++ {
		_, err = s.InsertAgentEvent(ctx, &AgentEvent{AgentName: "shared-agent", SessionID: &sessionB, EventType: "tool_use", Summary: "sibling activity"})
		require.NoError(t, err)
	}
	events, err := s.GetSessionStateEvents(ctx, []string{sessionA, sessionB})
	require.NoError(t, err)
	if len(events[sessionA]) != 1 || events[sessionA][0].EventType != "notification" {
		t.Fatalf("session A attention event was pruned by sibling activity: %+v", events[sessionA])
	}
	if len(events[sessionB]) != 500 {
		t.Fatalf("session B retention = %d, want 500", len(events[sessionB]))
	}
}

func TestGetAllEditedFileCounts(t *testing.T) {
	db := openTestDB(t)
	s := NewTaskStore(db)
	ctx := context.Background()

	// Helper to create a Write/Edit event with single-encoded detail_json
	// (matches production format after makeToolDetail root cause fix)
	insertEditEvent := func(agent, sessionID, toolName, filePath string) {
		detail := `{"file_path":"` + filePath + `"}`
		ev := &AgentEvent{
			AgentName:  agent,
			SessionID:  &sessionID,
			EventType:  "tool_use",
			ToolName:   &toolName,
			DetailJSON: &detail,
		}
		_, err := s.InsertAgentEvent(ctx, ev)
		require.NoError(t, err)
	}

	// Session 1: agent edits 3 distinct files
	insertEditEvent("agent-1", "sess-1", "Write", "/repo/main.go")
	insertEditEvent("agent-1", "sess-1", "Edit", "/repo/config.go")
	insertEditEvent("agent-1", "sess-1", "Write", "/repo/utils.go")

	// Session 1: duplicate edit to same file — should not increase count
	insertEditEvent("agent-1", "sess-1", "Edit", "/repo/main.go")

	// Session 2: agent edits 1 file
	insertEditEvent("agent-2", "sess-2", "Write", "/repo/server.go")

	// Session 3: agent has events but no Write/Edit (insert a non-edit event)
	readTool := "Read"
	_, err := s.InsertAgentEvent(ctx, &AgentEvent{
		AgentName: "agent-3",
		SessionID: strPtr("sess-3"),
		EventType: "tool_use",
		ToolName:  &readTool,
		Summary:   "Read a file",
	})
	require.NoError(t, err)

	// Get counts
	counts, err := s.GetAllEditedFileCounts(ctx)
	require.NoError(t, err)

	// Session 1: 3 distinct files (main.go edited twice, counted once)
	assert.Equal(t, 3, counts["sess-1"])

	// Session 2: 1 file
	assert.Equal(t, 1, counts["sess-2"])

	// Session 3: no Write/Edit events — should not appear in map
	_, exists := counts["sess-3"]
	assert.False(t, exists, "session with no Write/Edit events should not be in counts")

	// Non-existent session should not appear
	_, exists = counts["sess-999"]
	assert.False(t, exists)
}
