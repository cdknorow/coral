package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
)

// boundsRig: the task and wait routes with an agent token's identity ("Lead", an Orchestrator of "team") and a store to preload.
func boundsRig(t *testing.T) (call func(method, path string, body any) (int, map[string]any), bs *board.Store) {
	t.Helper()
	bs, err := boardtest.Open(t.TempDir() + "/board.db")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	h := NewBoardHandler(bs)
	t.Cleanup(h.WaitBackground)
	_, err = bs.Subscribe(context.Background(), "team", "Lead", "Orchestrator", "tmux-lead", nil, "mentions")
	require.NoError(t, err)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, withActor(req, Actor{Kind: ActorToken, Name: "Lead", Session: "tmux-lead"}))
		})
	})
	r.Post("/api/board/{project}/tasks", h.CreateTask)
	r.Patch("/api/board/{project}/tasks/{taskID}", h.UpdateTask)
	r.Post("/api/board/{project}/tasks/{taskID}/amend", h.AmendTask)
	r.Post("/api/board/{project}/waits", h.RegisterWait)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	call = func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	return call, bs
}

// #4602 M10: a task title is at most 512 bytes and a body 256 KiB (create, retitle, amend); a larger one is a 413 whose message
// says so and nothing is stored; the limit itself is allowed.
func TestTaskTextSizesAreBounded(t *testing.T) {
	call, bs := boundsRig(t)
	create := func(title, body string) (int, map[string]any) {
		return call("POST", "/api/board/team/tasks", map[string]any{"title": title, "body": body, "subscriber_id": "Lead"})
	}
	code, out := create(strings.Repeat("t", 512), strings.Repeat("b", 256<<10))
	require.Equal(t, 201, code, "%v", out)
	id := int64(out["id"].(float64))

	code, out = create(strings.Repeat("t", 513), "")
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "title_too_large", out["code"])
	assert.Contains(t, out["message"], "at most 512 bytes")
	assert.Equal(t, out["error"], out["message"])
	code, out = create("ok", strings.Repeat("b", 256<<10+1))
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "body_too_large", out["code"])
	assert.Contains(t, out["message"], "262144")
	tasks, err := bs.ListTasks(context.Background(), "team")
	require.NoError(t, err)
	assert.Len(t, tasks, 1, "nothing was stored by the refusals")

	code, out = call("PATCH", fmt.Sprintf("/api/board/team/tasks/%d", id), map[string]any{"title": strings.Repeat("t", 513), "subscriber_id": "Lead"})
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "title_too_large", out["code"])
	code, out = call("PATCH", fmt.Sprintf("/api/board/team/tasks/%d", id), map[string]any{"title": strings.Repeat("t", 512), "subscriber_id": "Lead"})
	assert.NotEqual(t, http.StatusRequestEntityTooLarge, code, "512 bytes is allowed: %v", out)
	code, out = call("POST", fmt.Sprintf("/api/board/team/tasks/%d/amend", id), map[string]any{"subscriber_id": "Lead", "base_revision": 1, "reason": "r", "changes": map[string]any{"body": strings.Repeat("b", 256<<10+1)}})
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "body_too_large", out["code"])
}

// A wait's reason is at most 4 KiB.
func TestWaitReasonIsBounded(t *testing.T) {
	call, _ := boundsRig(t)
	wait := func(reason string) (int, map[string]any) {
		return call("POST", "/api/board/team/waits", map[string]any{"subscriber_id": "Lead", "wait_type": "message", "target_id": "x", "reason": reason})
	}
	code, out := wait(strings.Repeat("r", 4<<10))
	assert.Equal(t, http.StatusCreated, code, "%v", out)
	code, out = wait(strings.Repeat("r", 4<<10+1))
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "reason_too_large", out["code"])
	assert.Contains(t, out["message"], "at most 4096 bytes")
}

// The 51st board an agent parks on is refused with a clear 409; the same board again replaces its wait.
func TestWaitCountPerSubscriberRefusalIsClear(t *testing.T) {
	call, _ := boundsRig(t)
	for i := 0; i < board.MaxActiveWaitsPerSubscriber; i++ {
		code, out := call("POST", fmt.Sprintf("/api/board/b%d/waits", i), map[string]any{"subscriber_id": "Lead", "wait_type": "message", "target_id": "x", "reason": "r"})
		require.Equal(t, http.StatusCreated, code, "%d: %v", i, out)
	}
	code, out := call("POST", "/api/board/b-extra/waits", map[string]any{"subscriber_id": "Lead", "wait_type": "message", "target_id": "x", "reason": "r"})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "too_many_waits", out["code"])
	assert.EqualValues(t, 50, out["max"])
	assert.Contains(t, out["message"], "at most 50 active waits")
	code, _ = call("POST", "/api/board/b3/waits", map[string]any{"subscriber_id": "Lead", "wait_type": "message", "target_id": "y", "reason": "again"})
	assert.Equal(t, http.StatusCreated, code, "the same board's wait is replaced, not added")
}

// An agent token may have 500 open tasks it created on a board; the 501st is a 409 that says so; finishing one makes room.
func TestOpenTasksPerAgentTokenAreBounded(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	call, bs := boundsRig(t)
	ctx := context.Background()
	for i := 0; i < maxOpenTasksPerAgent; i++ {
		_, err := bs.CreateTaskWithOpts(ctx, "team", fmt.Sprintf("task %d", i), "", "medium", "Lead", nil)
		require.NoError(t, err)
	}
	code, out := call("POST", "/api/board/team/tasks", map[string]any{"title": "one more", "subscriber_id": "Lead"})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "too_many_open_tasks", out["code"])
	assert.EqualValues(t, 500, out["max"])
	assert.Contains(t, out["message"], "at most 500 open tasks")
	tasks, err := bs.ListTasks(ctx, "team")
	require.NoError(t, err)
	assert.Len(t, tasks, 500, "nothing was stored")

	_, err = bs.CompleteTask(ctx, "team", tasks[0].ID, "Lead", nil)
	require.NoError(t, err)
	code, out = call("POST", "/api/board/team/tasks", map[string]any{"title": "one more", "subscriber_id": "Lead"})
	assert.Equal(t, http.StatusCreated, code, "a finished task makes room: %v", out)

	// Another board is its own count.
	_, err = bs.Subscribe(ctx, "other", "Lead", "Orchestrator", "tmux-lead", nil, "mentions")
	require.NoError(t, err)
	code, out = call("POST", "/api/board/other/tasks", map[string]any{"title": "elsewhere", "subscriber_id": "Lead"})
	assert.Equal(t, http.StatusCreated, code, "%v", out)
}
