package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/server/routes"
)

func captureTaskListStdout(t *testing.T, st *boardState) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	cmdTaskList(st, false)
	w.Close()
	os.Stdout = old
	return <-done
}

// The table 'task list' printed before pagination, from the full list in its order.
func legacyTaskListOutput(tasks []board.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-4s %-12s %-9s %-15s %s\n", "ID", "Status", "Priority", "Assignee", "Title")
	for _, task := range tasks {
		status := task.Status
		if task.Workflow.CompletionReview != nil && status == "in_progress" {
			status = "review_requested"
		}
		assignee := "—"
		if task.AssignedTo != nil && *task.AssignedTo != "" {
			assignee = *task.AssignedTo
		}
		fmt.Fprintf(&b, "#%-3.0f %-12s %-9s %-15s %s\n", float64(task.ID), status, task.Priority, assignee, task.Title)
	}
	return b.String()
}

// #3269: the task list endpoint is paged now; 'coral-board task list' still lists every
// task, in the same order and with the same columns, across pages (450 tasks = 3 pages).
func TestTaskList_ListsEveryTaskAcrossPages(t *testing.T) {
	isolateBoardEnv(t)
	ctx := context.Background()
	bs, err := board.NewStoreWithKey(t.TempDir()+"/board.db", "")
	require.NoError(t, err)
	defer bs.Close()
	priorities := []string{"low", "critical", "medium", "high"}
	for i := 0; i < 450; i++ {
		worker := fmt.Sprintf("worker%d", i) // one task in progress per worker
		task, err := bs.CreateTask(ctx, "team", fmt.Sprintf("task %d", i), "body", priorities[i%4], "lead", worker)
		require.NoError(t, err)
		switch i % 5 {
		case 0:
			_, err = bs.ClaimTask(ctx, "team", worker, task.ID)
			require.NoError(t, err)
			_, err = bs.CompleteTask(ctx, "team", task.ID, worker, nil)
		case 1:
			_, err = bs.ClaimTask(ctx, "team", worker, task.ID)
			require.NoError(t, err)
			if i%3 == 0 {
				_, err = bs.SubmitCompletionReview(ctx, "team", task.ID, worker, "check", "success", "a second look", nil)
			}
		}
		require.NoError(t, err)
	}
	full, err := bs.ListTasks(ctx, "team")
	require.NoError(t, err)

	h := routes.NewBoardHandler(bs)
	r := chi.NewRouter()
	pages := 0
	r.Get("/api/board/{project}/tasks", func(w http.ResponseWriter, req *http.Request) {
		pages++
		h.ListTasks(w, req)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	serverURL = srv.URL

	got := captureTaskListStdout(t, &boardState{Project: "team"})
	assert.Equal(t, legacyTaskListOutput(full), got)
	assert.Equal(t, 3, pages, "200 + 200 + 50")
	assert.Contains(t, got, "review_requested")
}

// A server from before pagination ignores the parameters and sends every task in full;
// the CLI reads that one answer and stops.
func TestTaskList_OlderServerFullList(t *testing.T) {
	isolateBoardEnv(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{
			{"id": 2, "status": "in_progress", "priority": "low", "assigned_to": "a", "title": "second", "workflow": map[string]any{"completion_review": map[string]any{}}},
			{"id": 1, "status": "pending", "priority": "high", "title": "first", "workflow": map[string]any{}},
		}})
	}))
	defer srv.Close()
	serverURL = srv.URL

	got := captureTaskListStdout(t, &boardState{Project: "team"})
	assert.Equal(t, 1, calls)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	require.Len(t, lines, 3)
	assert.Contains(t, lines[1], "first")
	assert.Contains(t, lines[2], "review_requested")
}
