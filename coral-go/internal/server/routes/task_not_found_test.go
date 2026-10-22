package routes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A task that does not exist is a 404 "no such task" from every task route, never a 400 with the database's own words ("sql: no rows in result set").
func TestTaskRoutesAnswer404ForANoSuchTask(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	project := "nosuch-test"
	_, err := handler.bs.Subscribe(context.Background(), project, "worker-1", "Developer", "claude-0000000c-3864-4864-8864-00000000000c", nil, "all")
	require.NoError(t, err)
	base := fmt.Sprintf("%s/api/board/%s/tasks/9999", server.URL, project)
	sub := `"subscriber_id":"worker-1"`
	want404 := map[string]bool{"/complete": true, "/submit-review": true, "/approve": true, "/reject": true, "/cancel": true}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/complete", `{` + sub + `,"message":"done"}`},
		{"POST", "/submit-review", `{` + sub + `,"reason":"check","message":"m"}`},
		{"POST", "/release-review", `{` + sub + `,"reason":"r"}`},
		{"POST", "/review", `{` + sub + `,"to":["QA"]}`},
		{"POST", "/approve", `{` + sub + `,"note":"ok"}`},
		{"POST", "/reject", `{` + sub + `,"note":"no"}`},
		{"POST", "/attach", `{` + sub + `}`},
		{"POST", "/send-back", `{` + sub + `,"note":"x"}`},
		{"POST", "/mark-landed", `{` + sub + `,"digests":["a"]}`},
		{"POST", "/cancel", `{` + sub + `}`},
		{"PATCH", "/amend", `{` + sub + `,"revision":1,"reason":"r","body":"b"}`},
		{"POST", "/reassign", `{` + sub + `,"to":"x"}`},
		{"POST", "/park", `{` + sub + `,"reason":"r"}`},
		{"GET", "", ``},
	} {
		req, _ := http.NewRequest(c.method, base+c.path, bytes.NewReader([]byte(c.body)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Logf("%s %s -> %d %s", c.method, c.path, resp.StatusCode, strings.TrimSpace(string(b)))
		assert.NotContains(t, string(b), "sql:", "%s %s leaks the database's words", c.method, c.path)
		assert.NotContains(t, string(b), "no rows", "%s %s", c.method, c.path)
		if want404[c.path] {
			assert.Equal(t, http.StatusNotFound, resp.StatusCode, "%s %s: %s", c.method, c.path, b)
			assert.Contains(t, string(b), "Task not found", "%s %s", c.method, c.path)
		}
	}
}

// Claiming a task by an id that does not exist does not leak the database's words either.
func TestClaimOfANoSuchTaskIsNotASQLError(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	project := "nosuch-claim"
	_, err := handler.bs.Subscribe(context.Background(), project, "worker-1", "Developer", "claude-0000000c-3864-4864-8864-00000000000c", nil, "all")
	require.NoError(t, err)
	resp, err := http.Post(fmt.Sprintf("%s/api/board/%s/tasks/claim", server.URL, project), "application/json", strings.NewReader(`{"subscriber_id":"worker-1","task_id":9999}`))
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.NotContains(t, string(b), "sql:")
	assert.NotContains(t, string(b), "no rows")
	assert.GreaterOrEqual(t, resp.StatusCode, 400)
	assert.Less(t, resp.StatusCode, 500, string(b))
}
