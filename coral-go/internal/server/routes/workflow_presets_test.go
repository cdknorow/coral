package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/board"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestWorkflowPresetAPIWorkerAndUserPermissions(t *testing.T) {
	server, h := setupBoardTestServer(t)
	_, err := h.bs.Subscribe(context.Background(), "team", "worker", "Developer", "", nil, nil, "all", false)
	require.NoError(t, err)
	r := chi.NewRouter()
	r.Get("/api/board/{project}/working-mode/presets", h.ListWorkflowPresets)
	r.Post("/api/board/{project}/working-mode/presets", h.SaveWorkflowPreset)
	r.Put("/api/board/{project}/working-mode/presets/{presetID}", h.SaveWorkflowPreset)
	r.Post("/api/board/{project}/working-mode/presets/{presetID}/reset", h.ResetWorkflowPreset)
	r.Put("/api/board/{project}/working-mode", h.SetWorkingMode)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/api/board/team/working-mode"+path, bytes.NewBufferString(body)))
		return w
	}
	require.Equal(t, 201, call("POST", "/presets", `{"id":"custom","name":"Custom","instructions":"Review","subscriber_id":"worker"}`).Code)
	require.Equal(t, 409, call("POST", "/presets", `{"id":"custom","name":"Duplicate","instructions":"Lost"}`).Code)
	require.Equal(t, 200, call("PUT", "/presets/custom", `{"name":"Renamed","instructions":"Updated","subscriber_id":"worker"}`).Code)
	require.Equal(t, 200, call("PUT", "", `{"mode":"custom","custom_instructions":"Extra","subscriber_id":"worker"}`).Code)
	w := call("GET", "/presets", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"instructions":"Updated\nExtra"`)
	require.Contains(t, w.Body.String(), `"name":"Renamed"`)
	require.Equal(t, 200, call("PUT", "/presets/worktrees", `{"instructions":"Override","subscriber_id":"worker"}`).Code)
	require.Equal(t, 200, call("POST", "/presets/worktrees/reset", `{"subscriber_id":"worker"}`).Code)
	require.Equal(t, 400, call("POST", "/presets/custom/reset", `{}`).Code)
	require.Equal(t, 400, call("PUT", "/presets/missing", `{"name":"Missing","instructions":"X"}`).Code)
	response := postJSON(t, server.URL+"/api/board/team/tasks", map[string]any{"subscriber_id": "worker", "title": "Denied"})
	defer response.Body.Close()
	require.Equal(t, 403, response.StatusCode)
}

func TestPromptInspectionMatchesLaunchBuilders(t *testing.T) {
	_, h := setupSystemTestServer(t)
	ctx := context.Background()
	for _, override := range []string{"", "Custom instructions for {board_name}."} {
		require.NoError(t, h.ss.SetSetting(ctx, "default_prompt_worker", override))
		require.NoError(t, h.ss.SetSetting(ctx, "default_prompt_orchestrator", override))
		w := httptest.NewRecorder()
		h.GetPromptInspection(w, httptest.NewRequest("GET", "/api/settings/prompt-inspection?board=example", nil))
		require.Equal(t, 200, w.Code)
		var got map[string]map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		settings := map[string]string{"default_prompt_worker": override, "default_prompt_orchestrator": override}
		for _, role := range []struct{ key, title, system, action string }{
			{"worker", "Worker", agent.DefaultWorkerSystemPrompt, agent.DefaultWorkerActionPrompt},
			{"orchestrator", "Orchestrator", agent.DefaultOrchestratorSystemPrompt, agent.DefaultOrchestratorActionPrompt},
		} {
			require.Equal(t, role.system, got[role.key]["system_default"])
			require.Equal(t, role.action, got[role.key]["action_default"])
			require.Equal(t, override, got[role.key]["override"])
			require.Equal(t, agent.BuildBoardSystemPrompt("example", role.title, "", settings, ""), got[role.key]["effective_system"])
			require.Equal(t, agent.BuildBoardActionPrompt("example", role.title, "", settings, ""), got[role.key]["effective_action"])
		}
		require.Equal(t, board.DefaultTaskWorkflowInstructions, got["task"]["default_instructions"])
		current, err := h.ss.GetSettings(ctx)
		require.NoError(t, err)
		require.Equal(t, override, current["default_prompt_worker"])
	}
}
