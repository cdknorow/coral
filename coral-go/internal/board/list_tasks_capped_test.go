package board

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4622: past the cap, the list keeps every open task and then the newest done ones (the coordination
// report reads it for the board's state), in the order the whole list has; the first rows by priority
// and id were the oldest done critical tasks.
func TestListTasksCappedKeepsOpenTasksThenTheNewestDone(t *testing.T) {
	ctx := context.Background()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	add := func(title, priority, status string) int64 {
		task, err := s.CreateTaskWithOpts(ctx, "b", title, "", priority, "Lead", nil)
		require.NoError(t, err)
		_, err = s.db.Exec(`UPDATE board_tasks SET status = ? WHERE id = ?`, status, task.ID)
		require.NoError(t, err)
		return task.ID
	}
	var doneIDs []int64
	for i := 0; i < 6; i++ { // old, critical, done
		doneIDs = append(doneIDs, add(fmt.Sprintf("old done %d", i), "critical", "completed"))
	}
	openLow := add("open low", "low", "pending")
	openHigh := add("open high", "high", "in_progress")
	skipped := add("skipped", "medium", "skipped")
	_ = skipped

	// Under the cap: all of them, as before.
	all, more, err := s.ListTasksCapped(ctx, "b", 20)
	require.NoError(t, err)
	assert.False(t, more)
	require.Len(t, all, 9)
	assert.Equal(t, []int64{doneIDs[0], doneIDs[1], doneIDs[2], doneIDs[3], doneIDs[4], doneIDs[5], openHigh, skipped, openLow}, taskIDs(all), "priority then id")

	// Over it: the 2 open tasks and the newest 2 done ones (the skipped and the newest completed), cut and marked.
	got, more, err := s.ListTasksCapped(ctx, "b", 4)
	require.NoError(t, err)
	assert.True(t, more)
	assert.Equal(t, []int64{doneIDs[5], openHigh, skipped, openLow}, taskIDs(got), "open first, then the newest done, in the usual order")

	// The open tasks alone are more than the cap: the first of them by priority and id.
	got, more, err = s.ListTasksCapped(ctx, "b", 1)
	require.NoError(t, err)
	assert.True(t, more)
	assert.Equal(t, []int64{openHigh}, taskIDs(got))

	// No cap: every task, as ListTasks.
	every, more, err := s.ListTasksCapped(ctx, "b", 0)
	require.NoError(t, err)
	assert.False(t, more)
	assert.Equal(t, taskIDs(all), taskIDs(every))
}

func taskIDs(tasks []Task) []int64 {
	out := make([]int64, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}
