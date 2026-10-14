package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/ptymanager"
)

type peekTerminal struct {
	ptymanager.SessionTerminal
}

func (peekTerminal) CaptureOutput(_ context.Context, name string, _ int, _, _ string) (string, error) {
	return "output of " + name, nil
}

// SEC-TUN-02 (#3410): through Coral Hub a peek never takes the caller's identity from the
// query (a View user may read every agent, the operator's decision, but not by claiming
// to be an orchestrator) and never touches task state. From this computer: unchanged.
func TestPeekThroughTheTunnel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	bs, err := board.NewStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	ctx := context.Background()
	_, err = bs.Subscribe(ctx, "p", "Orchestrator", "Orchestrator", "tmux-orch", nil, nil, "", true)
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "p", "Dev", "Backend Dev", "tmux-dev", nil, nil, "")
	require.NoError(t, err)
	task, err := bs.CreateTask(ctx, "p", "t", "", "medium", "Orchestrator", "Dev")
	require.NoError(t, err)
	_, err = bs.ClaimTask(ctx, "p", "Dev", task.ID)
	require.NoError(t, err)

	raw, err := sqlx.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })
	const old = "2000-01-01T00:00:00Z"
	activity := func() string {
		var v string
		require.NoError(t, raw.Get(&v, `SELECT COALESCE(last_activity_at, '') FROM board_tasks WHERE id = ?`, task.ID))
		return v
	}
	reset := func() {
		_, err := raw.Exec(`UPDATE board_tasks SET last_activity_at = ? WHERE id = ?`, old, task.ID)
		require.NoError(t, err)
	}

	h := NewBoardHandler(bs)
	h.SetTerminal(peekTerminal{})
	r := chi.NewRouter()
	r.Get("/api/board/{project}/peek", h.PeekAgent)
	peek := func(tunneled bool, query string) (int, map[string]any) {
		req := httptest.NewRequest("GET", "/api/board/p/peek?"+query, nil)
		if tunneled {
			stamp(req)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// From this computer: the caller's peek right is checked, and a peek counts as activity.
	reset()
	code, out := peek(false, "subscriber_id=Orchestrator&target=Dev")
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, "output of tmux-dev", out["output"])
	assert.NotEqual(t, old, activity(), "a local peek touches the target's task")
	code, _ = peek(false, "subscriber_id=Dev&target=Orchestrator")
	assert.Equal(t, http.StatusForbidden, code, "no peek right")
	code, _ = peek(false, "target=Dev")
	assert.Equal(t, http.StatusBadRequest, code)

	// Through the tunnel: the claimed identity is ignored (Orchestrator, a member without
	// the right, a stranger, or none), and the task is never touched.
	reset()
	for _, q := range []string{"subscriber_id=Orchestrator&target=Dev", "subscriber_id=Dev&target=Orchestrator", "subscriber_id=nobody&target=Dev", "target=Dev"} {
		code, out = peek(true, q)
		assert.Equal(t, http.StatusOK, code, q)
		assert.Contains(t, out["output"], "output of tmux-", q)
	}
	assert.Equal(t, old, activity(), "a peek through Coral Hub never changes task state")
	code, _ = peek(true, "subscriber_id=Orchestrator&target=ghost")
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = peek(true, "subscriber_id=Orchestrator")
	assert.Equal(t, http.StatusBadRequest, code)
}
