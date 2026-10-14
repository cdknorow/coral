package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getTaskList(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return resp.StatusCode, body
}

// #3269: GET /api/board/{project}/tasks is a light, paged list; full=1 keeps the old
// full response; bad queries are the caller's mistake (400).
func TestBoardListTasks_PagedLightListAndFullFallback(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	ctx := context.Background()
	bs := handler.bs
	body := strings.Repeat("x", 1000)
	for i := 0; i < 5; i++ {
		task, err := bs.CreateTask(ctx, "team", fmt.Sprintf("done %d", i), body, "medium", "lead", "worker")
		require.NoError(t, err)
		_, err = bs.ClaimTask(ctx, "team", "worker", task.ID)
		require.NoError(t, err)
		_, err = bs.CompleteTask(ctx, "team", task.ID, "worker", nil)
		require.NoError(t, err)
	}
	_, err := bs.CreateTask(ctx, "team", "open", body, "high", "lead")
	require.NoError(t, err)
	base := server.URL + "/api/board/team/tasks"

	status, page := getTaskList(t, base+"?limit=2")
	require.Equal(t, http.StatusOK, status)
	tasks := page["tasks"].([]any)
	assert.Len(t, tasks, 3, "the open task and two done tasks")
	assert.EqualValues(t, 1, page["total_open"])
	assert.EqualValues(t, 5, page["total_completed"])
	cursor, _ := page["next_cursor"].(string)
	require.NotEmpty(t, cursor)
	first := tasks[0].(map[string]any)
	assert.Equal(t, "open", first["title"])
	assert.NotContains(t, first, "body")
	assert.NotContains(t, first, "workflow")
	assert.Len(t, first["body_preview"], 200)

	seen := 2
	for cursor != "" {
		status, page = getTaskList(t, base+"?status=completed&limit=2&cursor="+cursor)
		require.Equal(t, http.StatusOK, status)
		seen += len(page["tasks"].([]any))
		cursor, _ = page["next_cursor"].(string)
	}
	assert.Equal(t, 5, seen, "the cursor walks every done task once")

	status, page = getTaskList(t, base+"?full=1")
	require.Equal(t, http.StatusOK, status)
	full := page["tasks"].([]any)
	assert.Len(t, full, 6, "full=1: every task")
	assert.Contains(t, full[0].(map[string]any), "body")
	assert.Contains(t, full[0].(map[string]any), "workflow")

	for _, q := range []string{"?status=done", "?status=completed&cursor=nope", "?limit=0", "?limit=x"} {
		status, page = getTaskList(t, base+q)
		assert.Equal(t, http.StatusBadRequest, status, q)
		assert.NotEmpty(t, page["error"], q)
	}
}

// #3342: a cursor from one board is refused on another (400), instead of offsetting its pages.
func TestBoardListTasks_CursorFromAnotherBoardIs400(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	ctx := context.Background()
	for _, board := range []string{"alpha", "beta"} {
		for i := 0; i < 3; i++ {
			task, err := handler.bs.CreateTask(ctx, board, fmt.Sprintf("%s %d", board, i), "", "medium", "lead", "worker")
			require.NoError(t, err)
			_, err = handler.bs.ClaimTask(ctx, board, "worker", task.ID)
			require.NoError(t, err)
			_, err = handler.bs.CompleteTask(ctx, board, task.ID, "worker", nil)
			require.NoError(t, err)
		}
	}
	status, page := getTaskList(t, server.URL+"/api/board/alpha/tasks?status=completed&limit=1")
	require.Equal(t, http.StatusOK, status)
	cursor, _ := page["next_cursor"].(string)
	require.NotEmpty(t, cursor)

	status, page = getTaskList(t, server.URL+"/api/board/beta/tasks?status=completed&limit=1&cursor="+cursor)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, page["error"], "another board")

	status, _ = getTaskList(t, server.URL+"/api/board/alpha/tasks?status=completed&limit=1&cursor="+cursor)
	assert.Equal(t, http.StatusOK, status, "on its own board the cursor still pages")
}
