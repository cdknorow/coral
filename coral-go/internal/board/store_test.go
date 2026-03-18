package board

import (
	"context"
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

func TestSubscribeAndList(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sub, err := s.Subscribe(ctx, "myproject", "agent-1", "Lead Dev", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "Lead Dev", sub.JobTitle)
	assert.Equal(t, "myproject", sub.Project)

	subs, err := s.ListSubscribers(ctx, "myproject")
	require.NoError(t, err)
	assert.Len(t, subs, 1)

	// Upsert same session updates job_title
	sub2, err := s.Subscribe(ctx, "myproject", "agent-1", "Senior Dev", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "Senior Dev", sub2.JobTitle)

	subs, _ = s.ListSubscribers(ctx, "myproject")
	assert.Len(t, subs, 1) // Still just one

	// Unsubscribe
	removed, err := s.Unsubscribe(ctx, "myproject", "agent-1")
	require.NoError(t, err)
	assert.True(t, removed)

	removed, _ = s.Unsubscribe(ctx, "myproject", "agent-1")
	assert.False(t, removed) // Already gone
}

func TestGetSubscription(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sub, err := s.GetSubscription(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, sub)

	s.Subscribe(ctx, "proj", "agent-1", "Dev", nil, nil)
	sub, err = s.GetSubscription(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.Equal(t, "proj", sub.Project)
}

func TestPostAndReadMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.Subscribe(ctx, "proj", "agent-1", "Dev A", nil, nil)
	s.Subscribe(ctx, "proj", "agent-2", "Dev B", nil, nil)

	// Agent-1 posts
	msg, err := s.PostMessage(ctx, "proj", "agent-1", "Hello team!")
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

	s.Subscribe(ctx, "proj", "agent-1", "Dev", nil, nil)
	s.PostMessage(ctx, "proj", "agent-1", "msg 1")
	s.PostMessage(ctx, "proj", "agent-1", "msg 2")

	messages, err := s.ListMessages(ctx, "proj", 100, 0)
	require.NoError(t, err)
	assert.Len(t, messages, 2)

	count, err := s.CountMessages(ctx, "proj")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestCheckUnread(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.Subscribe(ctx, "proj", "agent-1", "Dev A", nil, nil)
	s.Subscribe(ctx, "proj", "agent-2", "Dev B", nil, nil)

	// Post without mention — agent-2 should have 0 unread
	s.PostMessage(ctx, "proj", "agent-1", "Just a regular message")
	count, err := s.CheckUnread(ctx, "proj", "agent-2")
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Post with @all mention
	s.PostMessage(ctx, "proj", "agent-1", "@notify-all Important update!")
	count, err = s.CheckUnread(ctx, "proj", "agent-2")
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Post with @job_title mention
	s.PostMessage(ctx, "proj", "agent-1", "@Dev B please review this")
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

	s.Subscribe(ctx, "proj", "agent-1", "Dev A", nil, nil)
	s.Subscribe(ctx, "proj", "agent-2", "Dev B", nil, nil)

	s.PostMessage(ctx, "proj", "agent-1", "@all hello")

	counts, err := s.GetAllUnreadCounts(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, counts["agent-1"]) // Own message
	assert.Equal(t, 1, counts["agent-2"]) // Mentioned
}

func TestDeleteMessage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.Subscribe(ctx, "proj", "agent-1", "Dev", nil, nil)
	msg, _ := s.PostMessage(ctx, "proj", "agent-1", "delete me")

	removed, err := s.DeleteMessage(ctx, msg.ID)
	require.NoError(t, err)
	assert.True(t, removed)

	count, _ := s.CountMessages(ctx, "proj")
	assert.Equal(t, 0, count)
}

func TestListProjects(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.Subscribe(ctx, "proj-a", "agent-1", "Dev", nil, nil)
	s.Subscribe(ctx, "proj-b", "agent-2", "Dev", nil, nil)
	s.PostMessage(ctx, "proj-a", "agent-1", "hello")

	projects, err := s.ListProjects(ctx)
	require.NoError(t, err)
	assert.Len(t, projects, 2)
}

func TestDeleteProject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.Subscribe(ctx, "proj", "agent-1", "Dev", nil, nil)
	s.PostMessage(ctx, "proj", "agent-1", "hello")

	err := s.DeleteProject(ctx, "proj")
	require.NoError(t, err)

	subs, _ := s.ListSubscribers(ctx, "proj")
	assert.Empty(t, subs)
	count, _ := s.CountMessages(ctx, "proj")
	assert.Equal(t, 0, count)
}
