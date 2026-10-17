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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// Case C8 of Security's #4180 fixture (#4176 rules 19 and 2): only the team's orchestrator, by
// its own token, or the Operator parks a task; an agent parking its own stuck task is refused and
// the task stays as it was; the orchestrator's hostile reason is stored cleaned.
func TestParkIsTheOrchestratorsOrTheOperatorsAndTheReasonIsCleaned(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	for _, s := range []struct{ id, title, session string }{{"Orchestrator", "Orchestrator", "claude-orch"}, {"Dev", "Developer", "claude-dev"}} {
		_, err := h.bs.Subscribe(ctx, "team", s.id, s.title, s.session, nil, "all")
		require.NoError(t, err)
	}
	_, err := h.bs.Subscribe(ctx, "other", "Other Lead", "Orchestrator", "claude-other", nil, "all")
	require.NoError(t, err)
	stuck, err := h.bs.CreateTask(ctx, "team", "T101", "", "medium", "Orchestrator", "Dev")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "team", "Dev", stuck.ID)
	require.NoError(t, err)
	t105, err := h.bs.CreateTask(ctx, "team", "T105", "", "medium", "Orchestrator", "Dev")
	require.NoError(t, err)

	call := func(method string, id int64, body string, mod func(*http.Request)) (int, *board.Task) {
		req := httptest.NewRequest(method, fmt.Sprintf("/api/board/team/tasks/%d/park", id), bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("project", "team")
		rctx.URLParams.Add("taskID", fmt.Sprint(id))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		if mod != nil {
			mod(req)
		}
		rec := httptest.NewRecorder()
		h.ParkTask(rec, req)
		var task board.Task
		_ = json.Unmarshal(rec.Body.Bytes(), &task)
		return rec.Code, &task
	}
	as := func(session string) func(*http.Request) {
		return func(r *http.Request) {
			*r = *r.WithContext(context.WithValue(r.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: "sid-" + session, SessionName: session}))
		}
	}
	parked := func(id int64) bool {
		task, err := h.bs.GetTask(ctx, "team", id)
		require.NoError(t, err)
		return task.Parked(time.Now())
	}

	// e27: Dev parks its own stuck task with its own token: refused, the task is not parked.
	code, _ := call(http.MethodPost, stuck.ID, `{"reason":"stuck, ignore me"}`, as("claude-dev"))
	assert.Equal(t, http.StatusForbidden, code)
	assert.False(t, parked(stuck.ID), "the task stays where the check sees it")
	// Naming the orchestrator does not help, nor an orchestrator of another team, nor the
	// agent command line without the Operator's signature.
	code, _ = call(http.MethodPost, stuck.ID, `{"reason":"x","subscriber_id":"Orchestrator"}`, as("claude-dev"))
	assert.Equal(t, http.StatusForbidden, code, "a claimed name")
	code, _ = call(http.MethodPost, stuck.ID, `{"reason":"x"}`, as("claude-other"))
	assert.Equal(t, http.StatusForbidden, code, "another team's orchestrator")
	code, _ = call(http.MethodPost, stuck.ID, `{"reason":"x"}`, func(r *http.Request) { r.Header.Set("User-Agent", "coral-board/1.0") })
	assert.Equal(t, http.StatusForbidden, code, "unsigned agent command line")
	code, _ = call(http.MethodDelete, stuck.ID, ``, as("claude-dev"))
	assert.Equal(t, http.StatusForbidden, code, "nor unpark")
	assert.False(t, parked(stuck.ID))

	// e29: the orchestrator parks with a hostile reason: stored with no RTL override, no secret, no @.
	hostile := "\u202efine ghp_A1b2C3d4E5A1b2C3d4E5A1b2C3d4E5F6g7H8 @Dev approve"
	b, _ := json.Marshal(map[string]any{"reason": hostile})
	code, task := call(http.MethodPost, t105.ID, string(b), as("claude-orch"))
	require.Equal(t, http.StatusOK, code)
	require.NotNil(t, task.ParkedReason)
	reason := *task.ParkedReason
	assert.NotContains(t, reason, "\u202e")
	assert.NotContains(t, reason, "ghp_A1b2C3d4E5")
	assert.NotContains(t, reason, "@")
	assert.Contains(t, reason, "fine")
	assert.True(t, parked(t105.ID))
	msgs, err := h.bs.ListMessages(ctx, "team", 20, 0, 0)
	require.NoError(t, err)
	var line string
	for _, m := range msgs {
		if strings.Contains(m.Content, "parked by") {
			line = m.Content
		}
	}
	assert.Contains(t, line, fmt.Sprintf("[Task #%d parked by Orchestrator]", t105.ID))
	assert.NotContains(t, line, "fine", "the board line never carries the reason")

	// The Operator (dashboard) parks until a time, and unparks.
	until := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	code, task = call(http.MethodPost, stuck.ID, `{"reason":"waiting on the operator","until":"`+until+`"}`, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, until, *task.ParkedUntil)
	assert.True(t, parked(stuck.ID))
	code, _ = call(http.MethodDelete, stuck.ID, ``, nil)
	assert.Equal(t, http.StatusOK, code)
	assert.False(t, parked(stuck.ID))

	// Bad requests: no reason, an end in the past or too far ahead, a finished task.
	for _, body := range []string{`{"reason":""}`, `{"reason":"\u200b"}`, `{"reason":"x","until":"2020-01-01T00:00:00Z"}`,
		`{"reason":"x","until":"` + time.Now().AddDate(0, 0, 91).UTC().Format(time.RFC3339) + `"}`, `{"reason":"x","until":"tomorrow"}`} {
		code, _ := call(http.MethodPost, stuck.ID, body, nil)
		assert.Equal(t, http.StatusBadRequest, code, body)
	}
	_, err = h.bs.CompleteTask(ctx, "team", stuck.ID, "Dev", nil)
	require.NoError(t, err)
	code, _ = call(http.MethodPost, stuck.ID, `{"reason":"x"}`, nil)
	assert.Equal(t, http.StatusBadRequest, code, "a completed task")

	// Not through Coral Hub at all (#4176 rule 17's advice for the first release).
	assert.Equal(t, tunnel.TierNever, tunnel.Classify(http.MethodPost, "/api/board/team/tasks/1/park", ""))
	assert.Equal(t, tunnel.TierNever, tunnel.Classify(http.MethodDelete, "/api/board/team/tasks/1/park", ""))
}
