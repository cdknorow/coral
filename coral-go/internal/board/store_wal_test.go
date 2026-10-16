package board

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoardWALReadPoolNonBlocking(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "board_wal_test.db")

	store, err := NewStore(dbPath)
	require.NoError(t, err)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	project := "test-project"

	// Seed subscription, message, and task
	sub, err := store.Subscribe(ctx, project, "agent-1", "Agent One", "claude-1", nil, "all")
	require.NoError(t, err)
	require.NotNil(t, sub)

	msg, err := store.PostMessage(ctx, project, "agent-1", "First content", nil)
	require.NoError(t, err)
	require.NotNil(t, msg)

	task, err := store.CreateTask(ctx, project, "Task 1", "Task body", "medium", "agent-1")
	require.NoError(t, err)
	require.NotNil(t, task)

	// Begin a write transaction on the writer connection (s.db) and update without committing
	tx, err := store.db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, "UPDATE board_messages SET content = 'Uncommitted Change' WHERE id = ?", msg.ID)
	require.NoError(t, err)

	// Concurrently read via board methods that use reader()
	readDone := make(chan struct{})
	start := time.Now()

	go func() {
		defer close(readDone)

		// 1. ListMessages
		msgs, err := store.ListMessages(ctx, project, 50, 0, 0)
		assert.NoError(t, err)
		assert.Len(t, msgs, 1)
		assert.Equal(t, "First content", msgs[0].Content) // Reader sees committed snapshot

		// 2. ListMessagesBefore
		beforeMsgs, err := store.ListMessagesBefore(ctx, project, msg.ID+1, 10)
		assert.NoError(t, err)
		assert.Len(t, beforeMsgs, 1)

		// 3. ListTasks
		tasks, err := store.ListTasks(ctx, project)
		assert.NoError(t, err)
		assert.Len(t, tasks, 1)
		assert.Equal(t, "Task 1", tasks[0].Title)

		// 4. GetSubscription
		gotSub, err := store.GetSubscription(ctx, "agent-1")
		assert.NoError(t, err)
		assert.NotNil(t, gotSub)

		// 5. CountMessages
		count, err := store.CountMessages(ctx, project)
		assert.NoError(t, err)
		assert.Equal(t, 1, count)

		// 6. GetUnreadCountsForSessions (hot path for /api/sessions/live)
		counts, err := store.GetUnreadCountsForSessions(ctx, []string{"claude-1"})
		assert.NoError(t, err)
		assert.NotNil(t, counts)

		// 7. Direct query via store.Reader()
		var content string
		err = store.Reader().GetContext(ctx, &content, "SELECT content FROM board_messages WHERE id = ?", msg.ID)
		assert.NoError(t, err)
		assert.Equal(t, "First content", content)
	}()

	select {
	case <-readDone:
		duration := time.Since(start)
		assert.Less(t, duration, 200*time.Millisecond, "Read queries should complete quickly without blocking on writer transaction")
	case <-time.After(2 * time.Second):
		t.Fatal("Board read query blocked on active write transaction!")
	}

	require.NoError(t, tx.Commit())

	// After commit, reader sees the updated content
	msgs, err := store.ListMessages(ctx, project, 10, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "Uncommitted Change", msgs[0].Content)
}

func TestBoardWALReadPoolQueryOnlyRejection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "board_readonly_test.db")

	store, err := NewStore(dbPath)
	require.NoError(t, err)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Direct write on store.Reader() must fail with readonly error
	_, err = store.Reader().ExecContext(ctx, "INSERT INTO board_messages (project, subscriber_id, content) VALUES ('test', 's', 'c')")
	require.Error(t, err)
	assert.True(t, strings.Contains(strings.ToLower(err.Error()), "readonly") || strings.Contains(strings.ToLower(err.Error()), "read-only"),
		"Expected readonly error, got: %v", err)
}

func TestBoardWALReadPoolInMemoryFallback(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// In memory fallback must use same single connection for Reader()
	assert.Equal(t, store.db, store.Reader())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Creating task works
	task, err := store.CreateTask(ctx, "proj", "Mem Task", "Body", "medium", "agent")
	require.NoError(t, err)
	require.NotNil(t, task)

	tasks, err := store.ListTasks(ctx, "proj")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}
