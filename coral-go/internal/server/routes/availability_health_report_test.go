package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/board"
)

func TestAvailabilityHealthReport_BlockedTasks(t *testing.T) {
	blocked := func(id int64, deps ...string) board.Task {
		task := board.Task{ID: id, Status: "blocked"}
		for i, status := range deps {
			task.BlockedBy = append(task.BlockedBy, board.TaskDep{TaskID: int64(100 + i), Status: status})
		}
		return task
	}
	cases := []struct {
		name string
		task board.Task
		want []string
	}{
		{"no prerequisites", blocked(1), []string{"#1 is blocked but has no prerequisites"}},
		{"prerequisites done", blocked(2, "completed", "completed"), []string{"#2 is blocked after all prerequisites resolved"}},
		{"a prerequisite was skipped (canceled)", blocked(3, "completed", "skipped"), []string{"#3 is blocked after all prerequisites resolved", "#3 depends on canceled work"}},
		{"skipped while another is still open", blocked(4, "skipped", "in_progress"), []string{"#4 depends on canceled work"}},
		{"prerequisite still open", blocked(5, "pending"), []string{"No queue health issues detected."}},
		{"deleted upstream (empty status)", blocked(6, ""), []string{"No queue health issues detected."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, availabilityHealthReport([]board.Task{tc.task}))
		})
	}
}
