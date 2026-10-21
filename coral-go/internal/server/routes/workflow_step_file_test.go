package routes

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The dashboard reads a step's files from inside the data folder, which sensitivePath walls off as a
// whole: a local request is bounded by the step folder, a tunneled one is also refused sensitive names.
func TestGetStepFileServesTheRunsOwnFolderLocallyAndRefusesSensitiveNamesTunneled(t *testing.T) {
	dataDir := t.TempDir()
	ProtectDataDir(dataDir)
	cfg := config.Load(dataDir)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewWorkflowHandler(db, cfg)
	ctx := context.Background()
	wf, err := h.ws.CreateWorkflow(ctx, &store.Workflow{Name: "files", StepsJSON: `[{"name":"one","type":"shell","command":"true"}]`, DefaultAgentJSON: "{}", BaseBranch: "main", MaxDurationS: 60, Enabled: 1})
	require.NoError(t, err)
	run, err := h.ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)

	stepDir := filepath.Join(cfg.CoralDir(), "workflows", "runs", fmt.Sprint(run.ID), "step_0")
	require.NoError(t, os.MkdirAll(stepDir, 0o755))
	for name, body := range map[string]string{"stdout.txt": "hello", "id_rsa": "KEY"} {
		require.NoError(t, os.WriteFile(filepath.Join(stepDir, name), []byte(body), 0o644))
	}
	r := chi.NewRouter()
	r.Get("/api/workflows/runs/{runID}/steps/{stepIndex}/files/*", h.GetStepFile)
	get := func(name string, tunneled bool) int {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/workflows/runs/%d/steps/0/files/%s", run.ID, name), nil)
		if tunneled {
			req.Header.Set(tunnel.ViaHeader, "hub")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, get("stdout.txt", false), "local: the run's own file")
	require.Equal(t, http.StatusOK, get("id_rsa", false), "local: bounded by the step folder only")
	require.Equal(t, http.StatusOK, get("stdout.txt", true), "tunneled: an ordinary output file")
	require.Equal(t, http.StatusNotFound, get("id_rsa", true), "tunneled: a sensitive name is refused")
}
