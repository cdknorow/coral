package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4154: ListAgentTasksForSessions gives each session the tasks ListAgentTasks gives it (with the
// agent name filtered by the caller), in the same order and with the same workflow status.
func TestListAgentTasksForSessionsMatchesListAgentTasks(t *testing.T) {
	db, err := OpenWithKey(context.Background(), filepath.Join(t.TempDir(), "coral.db"), "")
	require.NoError(t, err)
	defer db.Close()
	s := NewTaskStore(db)
	ctx := context.Background()
	one, two, none := "s-one", "s-two", "s-none"
	type at struct {
		agent string
		sid   *string
		title string
	}
	for _, c := range []at{{"dev", &one, "B"}, {"dev", &one, "A"}, {"dev", &two, "C"}, {"other", &one, "D"}, {"dev", nil, "E"}} {
		_, err := s.CreateAgentTask(ctx, c.agent, c.title, c.sid, nil)
		require.NoError(t, err)
	}
	all, err := s.ListAgentTasks(ctx, "dev", &one)
	require.NoError(t, err)
	require.NoError(t, s.UpdateAgentTask(ctx, all[0].ID, nil, ptrInt(1), nil)) // one completed

	got, err := s.ListAgentTasksForSessions(ctx, []string{one, two, none, one})
	require.NoError(t, err)
	for _, c := range []struct{ agent, sid string }{{"dev", one}, {"dev", two}, {"other", one}, {"dev", none}} {
		sid := c.sid
		want, err := s.ListAgentTasks(ctx, c.agent, &sid)
		require.NoError(t, err)
		var mine []AgentTask
		for _, task := range got[c.sid] {
			if task.AgentName == c.agent {
				mine = append(mine, task)
			}
		}
		assert.Equal(t, want, mine, "%s on %s", c.agent, c.sid)
	}
	assert.Empty(t, got[none])
}

func ptrInt(v int) *int { return &v }
