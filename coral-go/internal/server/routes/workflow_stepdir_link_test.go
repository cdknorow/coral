package routes

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #7155 (CodeQL #753, Codex Test's #7151): the step-file route bounded a file by the step folder it resolved, so a step folder that is itself a link to a folder outside the runs folder (or a run
// folder that is) made every file there readable, tunnelled or not. The step folder, resolved, must lie inside the resolved runs folder too.
func TestWorkflowStepFileRouteRefusesAStepFolderThatLinksOutsideTheRunsFolder(t *testing.T) {
	cfg := config.Load(t.TempDir())
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewWorkflowHandler(db, cfg)
	ctx := context.Background()
	wf, err := h.ws.CreateWorkflow(ctx, &store.Workflow{Name: "link", StepsJSON: `[{"name":"one","type":"shell","command":"true"},{"name":"two","type":"shell","command":"true"}]`, DefaultAgentJSON: "{}", BaseBranch: "main", MaxDurationS: 60, Enabled: 1})
	require.NoError(t, err)
	run, err := h.ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("OUTSIDE-MARKER"), 0o644))
	runDir := filepath.Join(cfg.CoralDir(), "workflows", "runs", fmt.Sprint(run.ID))
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	// step_0 is a link to an outside folder; step_1 is a real folder with one file (the control).
	if err := os.Symlink(outside, filepath.Join(runDir, "step_0")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need a privilege here: ", err)
		}
		require.NoError(t, err)
	}
	real1 := filepath.Join(runDir, "step_1")
	require.NoError(t, os.MkdirAll(real1, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(real1, "ok.txt"), []byte("INSIDE"), 0o644))

	r := chi.NewRouter()
	r.Get("/api/workflows/runs/{runID}/steps/{stepIndex}/files/*", h.GetStepFile)
	server := httptest.NewServer(r)
	defer server.Close()
	get := func(step int, name string, tunnelled bool) (int, string) {
		req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/workflows/runs/%d/steps/%d/files/%s", server.URL, run.ID, step, name), nil)
		require.NoError(t, err)
		if tunnelled {
			req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, tunnelled := range []bool{false, true} {
		code, body := get(0, "outside.txt", tunnelled)
		assert.Equal(t, http.StatusNotFound, code, "a step folder linked outside the runs folder serves nothing (tunnelled: %v)", tunnelled)
		assert.NotContains(t, body, "OUTSIDE-MARKER")
		code, body = get(1, "ok.txt", tunnelled)
		assert.Equal(t, http.StatusOK, code, "a real step folder still serves its files (tunnelled: %v)", tunnelled)
		assert.Equal(t, "INSIDE", body)
	}

	// The whole run folder linked outside works the same way.
	run2, err := h.ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)
	outRun := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outRun, "step_0"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outRun, "step_0", "outside.txt"), []byte("OUTSIDE-MARKER"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.CoralDir(), "workflows", "runs"), 0o755))
	require.NoError(t, os.Symlink(outRun, filepath.Join(cfg.CoralDir(), "workflows", "runs", fmt.Sprint(run2.ID))))
	for _, tunnelled := range []bool{false, true} {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/workflows/runs/%d/steps/0/files/outside.txt", server.URL, run2.ID), nil)
		if tunnelled {
			req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "a run folder linked outside the runs folder serves nothing (tunnelled: %v)", tunnelled)
		assert.NotContains(t, string(b), "OUTSIDE-MARKER")
	}
}
