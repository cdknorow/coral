package routes

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
)

func queuePatch(name, digest string) board.TaskArtifact {
	return board.TaskArtifact{Name: name, Kind: board.PatchKind, URI: "coral://artifacts/" + digest, Digest: digest, Revision: "base1"}
}

// attach and send-back through the HTTP API (#5689): who may call them, and what they do to the task.
func TestMergeQueueAttachAndSendBackAPI(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	for name, role := range map[string]string{"lead": "Orchestrator", "worker": "Developer", "rev": "QA"} {
		_, err := h.bs.Subscribe(ctx, "mq", name, role, name+"-session", nil, "all")
		require.NoError(t, err)
	}
	d1, d2 := strings.Repeat("a", 64), strings.Repeat("b", 64)
	task, err := h.bs.CreateTask(ctx, "mq", "Fix", "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "mq", "worker", task.ID)
	require.NoError(t, err)
	base := fmt.Sprintf("%s/api/board/mq/tasks/%d/", server.URL, task.ID)
	call := func(action string, body map[string]any, want int) {
		t.Helper()
		r := postJSON(t, base+action, body)
		defer r.Body.Close()
		require.Equal(t, want, r.StatusCode, action)
	}
	call("attach", map[string]any{"subscriber_id": "worker", "artifacts": []board.TaskArtifact{queuePatch("fix", d1)}}, http.StatusBadRequest) // not in review yet
	call("submit-review", map[string]any{"subscriber_id": "worker", "reason": "patch", "artifacts": []board.TaskArtifact{queuePatch("fix", d1)}}, 200)
	call("review", map[string]any{"subscriber_id": "lead", "reviewers": []string{"rev"}}, 200)
	call("attach", map[string]any{"subscriber_id": "worker", "artifacts": []board.TaskArtifact{queuePatch("fix", d2)}}, 200)
	call("attach", map[string]any{"subscriber_id": "worker", "artifacts": []board.TaskArtifact{}}, http.StatusBadRequest)
	got, err := h.bs.GetTask(ctx, "mq", task.ID)
	require.NoError(t, err)
	require.Len(t, got.Workflow.CompletionReview.Artifacts, 2)
	call("send-back", map[string]any{"subscriber_id": "lead", "note": "not approved yet"}, http.StatusBadRequest) // an open round is not sent back
	call("approve", map[string]any{"subscriber_id": "rev"}, 200)
	call("send-back", map[string]any{"subscriber_id": "worker", "note": "x"}, http.StatusForbidden)
	call("send-back", map[string]any{"subscriber_id": "lead"}, http.StatusBadRequest)
	done, err := h.bs.GetTask(ctx, "mq", task.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", done.Status, "the last approval completes the task")
	call("send-back", map[string]any{"subscriber_id": "lead", "note": "conflict in a.go"}, 200)
	got, err = h.bs.GetTask(ctx, "mq", task.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)
	require.Equal(t, "worker", *got.AssignedTo)
}
