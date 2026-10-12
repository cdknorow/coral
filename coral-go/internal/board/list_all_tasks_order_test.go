package board

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Tasks created within the same timestamp come back newest id first, every time.
func TestListAllTasksBreaksCreatedAtTiesByID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var ids []int64
	for i := 0; i < 5; i++ {
		task, err := s.CreateTask(ctx, "proj", "t", "", "medium", "Orchestrator")
		require.NoError(t, err)
		ids = append(ids, task.ID)
	}
	_, err := s.db.Exec(`UPDATE board_tasks SET created_at = '2026-10-12T00:00:00Z'`)
	require.NoError(t, err)
	tasks, err := s.ListAllTasks(ctx, 3)
	require.NoError(t, err)
	got := []int64{tasks[0].ID, tasks[1].ID, tasks[2].ID}
	require.Equal(t, []int64{ids[4], ids[3], ids[2]}, got)
}

// TestHealthScanTaskReadPerf compares the board health monitor's task read
// before (ListTasks) and after (ListOpenTasks) on a long-lived board. Skipped
// unless CORAL_PERF_BOARD is set.
func TestHealthScanTaskReadPerf(t *testing.T) {
	if testing.Short() || !perfBoardEnabled() {
		t.Skip("set CORAL_PERF_BOARD=1 to run")
	}
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 5000; i++ {
		task, err := s.CreateTask(ctx, "big", "task", "body", "medium", "Orchestrator")
		require.NoError(t, err)
		if i%50 != 0 { // 2% stay open
			_, err = s.db.Exec(`UPDATE board_tasks SET status = 'completed' WHERE id = ?`, task.ID)
			require.NoError(t, err)
		}
	}
	timeIt := func(name string, fn func() int) {
		n := fn()
		start := time.Now()
		for i := 0; i < 5; i++ {
			fn()
		}
		t.Logf("%-14s %8s/call  tasks=%d", name, (time.Since(start) / 5).Round(100*time.Microsecond), n)
	}
	timeIt("ListTasks", func() int { ts, _ := s.ListTasks(ctx, "big"); return len(ts) })
	timeIt("ListOpenTasks", func() int { ts, _ := s.ListOpenTasks(ctx, "big"); return len(ts) })
}

func perfBoardEnabled() bool { return os.Getenv("CORAL_PERF_BOARD") != "" }

// TestListProjectsEnrichedPerf times the history board list on 50 boards with
// 50k messages. Skipped unless CORAL_PERF_BOARD is set.
func TestListProjectsEnrichedPerf(t *testing.T) {
	if !perfBoardEnabled() {
		t.Skip("set CORAL_PERF_BOARD=1 to run")
	}
	s := testStore(t)
	ctx := context.Background()
	tx, err := s.db.Beginx()
	require.NoError(t, err)
	for i := 0; i < 50000; i++ {
		_, err := tx.Exec(`INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at) VALUES (?, 'a', 'a', 'x', ?)`,
			"board-"+strconv.Itoa(i%50), time.Date(2026, 10, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	got, err := s.ListProjectsEnriched(ctx)
	require.NoError(t, err)
	require.Len(t, got, 50)
	start := time.Now()
	for i := 0; i < 10; i++ {
		_, _ = s.ListProjectsEnriched(ctx)
	}
	t.Logf("ListProjectsEnriched (50 boards, 50k msgs): %s/call; first=%v last=%v", (time.Since(start) / 10).Round(100*time.Microsecond), *got[0].FirstMessageAt, *got[0].LastMessageAt)
}

func TestListProjectsEnrichedFirstLastAndOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := sub(s, ctx, "quiet", "Dev", "Dev") // a board with subscribers but no messages
	require.NoError(t, err)
	for _, m := range []struct{ project, at string }{
		{"old", "2026-10-01T00:00:00Z"}, {"old", "2026-10-01T01:00:00Z"},
		{"new", "2026-10-02T00:00:00Z"}, {"new", "2026-10-02T05:00:00Z"}, {"new", "2026-10-02T09:00:00Z"},
	} {
		_, err := s.db.Exec(`INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at) VALUES (?, 'a', 'a', 'x', ?)`, m.project, m.at)
		require.NoError(t, err)
	}
	got, err := s.ListProjectsEnriched(ctx)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, "new", got[0].Project, "most recent activity first")
	require.Equal(t, 3, got[0].MessageCount)
	require.Equal(t, "2026-10-02T00:00:00Z", *got[0].FirstMessageAt)
	require.Equal(t, "2026-10-02T09:00:00Z", *got[0].LastMessageAt)
	require.Equal(t, "old", got[1].Project)
	require.Equal(t, "quiet", got[2].Project)
	require.Nil(t, got[2].FirstMessageAt)
	require.Equal(t, 1, got[2].SubscriberCount)
}
