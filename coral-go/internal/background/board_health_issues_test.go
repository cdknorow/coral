package background

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
)

func TestTaskQueueIssues(t *testing.T) {
	blocked := func(id int64, deps ...string) board.Task {
		task := board.Task{ID: id, Status: "blocked"}
		for i, st := range deps {
			task.BlockedBy = append(task.BlockedBy, board.TaskDep{TaskID: int64(100 + i), Status: st})
		}
		return task
	}
	cases := []struct {
		name string
		task board.Task
		want []string
	}{
		{"no prerequisites", blocked(1), []string{"#1 is blocked but has no prerequisites; unblock or cancel it"}},
		{"all completed", blocked(2, "completed", "completed"), []string{"#2 is blocked but all prerequisites are resolved; refresh its dependency state"}},
		{"a prerequisite was canceled (skipped)", blocked(3, "completed", "skipped"), []string{
			"#3 is blocked but all prerequisites are resolved; refresh its dependency state",
			"#3 depends on canceled work; rewire or cancel it"}},
		{"canceled while another is open", blocked(4, "skipped", "in_progress"), []string{"#4 depends on canceled work; rewire or cancel it"}},
		{"still waiting", blocked(5, "pending"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues, _, _ := taskQueueIssues([]board.Task{tc.task})
			require.Equal(t, tc.want, issues)
		})
	}

	// Load counts, planned work and the parallel hint are unchanged.
	a, b := "A", "B"
	issues, assigned, planned := taskQueueIssues([]board.Task{
		{ID: 10, Status: "in_progress", AssignedTo: &a}, {ID: 11, Status: "in_progress", AssignedTo: &a},
		{ID: 12, Status: "in_progress", AssignedTo: &a}, {ID: 13, Status: "in_progress", AssignedTo: &b},
		{ID: 14, Status: "pending", AssignedTo: &b},
		{ID: 15, Status: "pending"}, {ID: 16, Status: "pending"},
	})
	require.Equal(t, map[string]int{"A": 3, "B": 1}, assigned)
	require.Equal(t, 1, planned)
	require.Equal(t, []string{"assignment load is imbalanced across agents", "2 unblocked tasks can potentially run in parallel"}, issues)
}
