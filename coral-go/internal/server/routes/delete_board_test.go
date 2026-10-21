package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
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

func deleteBoardReq(t *testing.T, h *BoardHandler, project string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/board/"+project, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", project)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.DeleteBoard(rec, req)
	return rec
}

// listFailsTerminal is a terminal whose pane list cannot be read.
type listFailsTerminal struct{ *mockSessionTerminal }

func (listFailsTerminal) ListSessions(context.Context) ([]ptymanager.PaneInfo, error) {
	return nil, errors.New("tmux is not answering")
}

// #5043: a board's delete is refused (409) while agents are running on it: they would stay live in a sidebar group named with the raw board id. It says how many and which, and
// deletes nothing. A member that is asleep or gone (no terminal), and an agent running on another board, do not count; once the running one is stopped the delete goes through.
func TestDeleteBoardIsRefusedWhileItsAgentsRun(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	term := newMockTerminal()
	h.SetTerminal(term)
	for _, s := range []struct{ project, id, title, session string }{
		{"busy", "Boss", "Orchestrator", "claude-aaaaaaaa-0000-4000-8000-000000000001"},
		{"busy", "QA", "QA Engineer", "claude-aaaaaaaa-0000-4000-8000-000000000002"},
		{"busy", "Sleeper", "Docs", "claude-aaaaaaaa-0000-4000-8000-000000000003"}, // asleep: no terminal
		{"elsewhere", "Other", "Dev", "claude-aaaaaaaa-0000-4000-8000-000000000004"},
	} {
		_, err := h.bs.Subscribe(ctx, s.project, s.id, s.title, s.session, nil, "all")
		require.NoError(t, err)
		if s.id != "Sleeper" {
			term.sessions[s.session] = &ptymanager.PaneInfo{SessionName: s.session}
		}
	}
	_, err := h.bs.PostMessage(ctx, "busy", "Boss", "still here", nil)
	require.NoError(t, err)

	rec := deleteBoardReq(t, h, "busy")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var body struct {
		Error  string   `json:"error"`
		Code   string   `json:"code"`
		Agents []string `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "board_has_running_agents", body.Code)
	assert.ElementsMatch(t, []string{"Orchestrator", "QA Engineer"}, body.Agents, "the running members, by name; not the sleeper, not the other board's agent")
	assert.Regexp(t, `^2 agents are still on this board \((Orchestrator, QA Engineer|QA Engineer, Orchestrator)\): kill them or move them first$`, body.Error)
	count, _ := h.bs.CountMessages(ctx, "busy")
	assert.Equal(t, 1, count, "a refused delete deletes nothing")

	delete(term.sessions, "claude-aaaaaaaa-0000-4000-8000-000000000001")
	rec = deleteBoardReq(t, h, "busy")
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "1 agent is still on this board (QA Engineer): kill it or move it first")

	delete(term.sessions, "claude-aaaaaaaa-0000-4000-8000-000000000002")
	rec = deleteBoardReq(t, h, "busy")
	require.Equal(t, http.StatusOK, rec.Code, "with nothing running the delete goes through: "+rec.Body.String())
	count, _ = h.bs.CountMessages(ctx, "busy")
	assert.Equal(t, 0, count)
	subs, _ := h.bs.ListSubscribers(ctx, "elsewhere")
	assert.Len(t, subs, 1, "the other board is untouched")
}

// #5043: when the running terminals cannot be listed, the delete is refused (503) and nothing is deleted: a check that could not be made is not a pass.
func TestDeleteBoardIsRefusedWhenTheRunningAgentsCannotBeChecked(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	h.SetTerminal(listFailsTerminal{newMockTerminal()})
	_, err := h.bs.Subscribe(ctx, "unknown", "Boss", "Orchestrator", "claude-aaaaaaaa-0000-4000-8000-000000000009", nil, "all")
	require.NoError(t, err)
	_, err = h.bs.PostMessage(ctx, "unknown", "Boss", "kept", nil)
	require.NoError(t, err)

	rec := deleteBoardReq(t, h, "unknown")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "running_agents_unknown")
	count, _ := h.bs.CountMessages(ctx, "unknown")
	assert.Equal(t, 1, count)

	// A board nobody is a member of has nothing to check, so an unreadable pane list does not hold it up.
	rec = deleteBoardReq(t, h, "empty-board")
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
