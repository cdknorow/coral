package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClaimAPIExplainsMissingArtifact(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	up, err := h.bs.CreateTask(ctx, "team", "Diagnosis", "", "medium", "orch")
	require.NoError(t, err)
	_, err = h.bs.CompleteTaskWithArtifacts(ctx, "team", up.ID, "dev", nil, "success", []board.TaskArtifact{{Name: "diagnostic", Content: "evidence"}, {Name: "verification", Content: "evidence"}})
	require.NoError(t, err)
	down, err := h.bs.CreateTaskWithOpts(ctx, "team", "Follow-up", "", "medium", "orch", &board.CreateTaskOpts{BlockedBy: []board.TaskDep{{TaskID: up.ID, RequiredArtifacts: []string{"candidate", "verification"}}}}, "dev")
	require.NoError(t, err)
	for _, id := range []int64{0, down.ID} {
		data, _ := json.Marshal(map[string]any{"subscriber_id": "dev", "task_id": id})
		resp, err := http.Post(server.URL+"/api/board/team/tasks/claim", "application/json", bytes.NewReader(data))
		require.NoError(t, err)
		var result struct {
			Error   string               `json:"error"`
			Blocked []board.TaskBlockage `json:"blocked_tasks"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		resp.Body.Close()
		if id == 0 {
			require.Equal(t, 404, resp.StatusCode)
		} else {
			require.Equal(t, 400, resp.StatusCode)
		}
		require.Len(t, result.Blocked, 1)
		require.Equal(t, down.ID, result.Blocked[0].ID)
		require.Equal(t, []string{fmt.Sprintf("#%d: missing required artifacts: candidate", up.ID)}, result.Blocked[0].Reasons)
	}
}

func TestPersonalClaimAPIExplainsMissingArtifact(t *testing.T) {
	server, _, _, ss := setupSessionsTestServer(t)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: "blockage-agent", AgentName: "dev", AgentType: "codex", WorkingDir: "/tmp"}))
	post := func(path string, body map[string]any, want int) map[string]any {
		t.Helper()
		body["session_id"] = "blockage-agent"
		data, _ := json.Marshal(body)
		resp, err := http.Post(server.URL+"/api/agent/tasks"+path, "application/json", bytes.NewReader(data))
		require.NoError(t, err)
		defer resp.Body.Close()
		var result map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		require.Equal(t, want, resp.StatusCode, result)
		return result
	}
	up := post("", map[string]any{"title": "Diagnosis"}, 201)
	upID := int64(up["id"].(float64))
	post(fmt.Sprintf("/%d/complete", upID), map[string]any{"artifacts": []board.TaskArtifact{{Name: "diagnostic", Content: "evidence"}}}, 200)
	down := post("", map[string]any{"title": "Consumer", "blocked_by": []board.TaskDep{{TaskID: upID, RequiredArtifacts: []string{"candidate"}}}}, 201)
	result := post("/claim", map[string]any{}, 404)
	blocked := result["blocked_tasks"].([]any)
	require.Len(t, blocked, 1)
	require.Contains(t, blocked[0].(map[string]any)["reasons"].([]any)[0], "missing required artifacts: candidate")
	result = post("/claim", map[string]any{"task_id": down["id"]}, 400)
	require.Len(t, result["blocked_tasks"].([]any), 1)
}
