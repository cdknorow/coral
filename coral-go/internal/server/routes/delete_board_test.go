package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4966: DELETE /api/board/{name} deletes the whole board: its tasks go with its messages, and another board is left alone.
func TestDeleteBoardRemovesItsTasks(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	for _, p := range []string{"doomed", "kept"} {
		_, err := h.bs.Subscribe(ctx, p, "Boss", "Orchestrator", "claude-"+p, nil, "all")
		require.NoError(t, err)
		_, err = h.bs.PostMessage(ctx, p, "Boss", "hello", nil)
		require.NoError(t, err)
		_, err = h.bs.CreateTask(ctx, p, "work", "", "high", "Boss")
		require.NoError(t, err)
	}
	h.mu.Lock()
	h.paused["doomed"] = true
	h.mu.Unlock()
	req := httptest.NewRequest(http.MethodDelete, "/api/board/doomed", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", "doomed")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.DeleteBoard(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	gone, err := h.bs.ListTasks(ctx, "doomed")
	require.NoError(t, err)
	assert.Empty(t, gone, "the deleted board's tasks are gone")
	count, _ := h.bs.CountMessages(ctx, "doomed")
	assert.Equal(t, 0, count)
	h.mu.Lock()
	_, stillPaused := h.paused["doomed"]
	h.mu.Unlock()
	assert.False(t, stillPaused, "the in-memory pause goes once the board is deleted")
	kept, err := h.bs.ListTasks(ctx, "kept")
	require.NoError(t, err)
	assert.Len(t, kept, 1, "the other board keeps its task")
}
