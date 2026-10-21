package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// Deleting a workflow removes its runs' directories, not only their records
// (audit MED-04); another workflow's runs stay.
func TestDeleteWorkflowRemovesItsRunDirectories(t *testing.T) {
	cfg := config.Load(t.TempDir())
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewWorkflowHandler(db, cfg)
	ctx := context.Background()
	mk := func(name string) *store.Workflow {
		wf, err := h.ws.CreateWorkflow(ctx, &store.Workflow{Name: name, StepsJSON: `[]`, DefaultAgentJSON: "{}", BaseBranch: "main", MaxDurationS: 60, Enabled: 1})
		require.NoError(t, err)
		return wf
	}
	runDir := func(id int64) string {
		return filepath.Join(cfg.CoralDir(), "workflows", "runs", strconv.FormatInt(id, 10))
	}
	mkRun := func(wfID int64) int64 {
		run, err := h.ws.CreateWorkflowRun(ctx, wfID, "api", nil)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(runDir(run.ID), "step_0"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(runDir(run.ID), "step_0", "stdout.txt"), []byte("x"), 0o644))
		return run.ID
	}
	gone, keep := mk("gone"), mk("keep")
	goneRuns := []int64{mkRun(gone.ID), mkRun(gone.ID)}
	keepRun := mkRun(keep.ID)

	r := chi.NewRouter()
	r.Delete("/api/workflows/{workflowID}", h.DeleteWorkflow)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/workflows/"+strconv.FormatInt(gone.ID, 10), nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	for _, id := range goneRuns {
		assert.NoDirExists(t, runDir(id))
	}
	assert.DirExists(t, runDir(keepRun))
	ids, err := h.ws.WorkflowRunIDs(ctx, gone.ID)
	require.NoError(t, err)
	assert.Empty(t, ids, "the run records went with the workflow")
}

// A workflow's repo_path that is a file says so; one that doesn't exist says
// that (QA's note on #3425: a file used to answer "does not exist").
func TestWorkflowRepoPathErrorsNameTheProblem(t *testing.T) {
	cfg := config.Load(t.TempDir())
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewWorkflowHandler(db, cfg)
	r := chi.NewRouter()
	r.Post("/api/workflows", h.CreateWorkflow)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	file := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	for _, tc := range []struct{ path, want string }{
		{file, "repo_path is not a directory"},
		{filepath.Join(t.TempDir(), "missing"), "repo_path does not exist"},
	} {
		code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/workflows", map[string]any{
			"name": "wf", "repo_path": tc.path, "steps": []map[string]any{{"name": "one", "type": "shell", "command": "true"}},
		})
		assert.Equal(t, http.StatusBadRequest, code, tc.path)
		assert.Equal(t, tc.want, body["error"], tc.path)
	}
}
