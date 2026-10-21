package routes

import (
	"context"
	"encoding/json"
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

// The queue read and mark-landed through the HTTP API (#5689): the queue shows the approved newest patch, mark-landed is the orchestrator's
// (or the Operator's), refuses a digest nobody approved, and takes the task out of the queue.
func TestMergeQueueReadAndMarkLandedAPI(t *testing.T) {
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
	queue := func() []map[string]any {
		t.Helper()
		resp, err := http.Get(server.URL + "/api/board/mq/queue")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var out struct {
			Queue []map[string]any `json:"queue"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out.Queue
	}
	call("submit-review", map[string]any{"subscriber_id": "worker", "reason": "patch", "artifacts": []board.TaskArtifact{queuePatch("fix", d1)}}, 200)
	call("review", map[string]any{"subscriber_id": "lead", "reviewers": []string{"rev"}}, 200)
	require.Empty(t, queue(), "a task in review is not in the queue")
	call("attach", map[string]any{"subscriber_id": "worker", "artifacts": []board.TaskArtifact{queuePatch("fix", d2)}}, 200)
	call("approve", map[string]any{"subscriber_id": "rev", "round": 1}, http.StatusConflict) // the attach opened round 2: the old round is refused
	call("approve", map[string]any{"subscriber_id": "rev", "round": 2}, 200)
	q := queue()
	require.Len(t, q, 1)
	patches := q[0]["patches"].([]any)
	require.Len(t, patches, 1, "only the newest row of the name")
	require.Equal(t, d2, patches[0].(map[string]any)["digest"])
	// who may mark it landed, and what
	call("mark-landed", map[string]any{"subscriber_id": "worker", "digests": []string{d2}}, http.StatusForbidden)
	call("mark-landed", map[string]any{"subscriber_id": "rev", "digests": []string{d2}}, http.StatusForbidden)
	call("mark-landed", map[string]any{"subscriber_id": "lead", "digests": []string{d1}}, http.StatusBadRequest) // the superseded digest
	call("mark-landed", map[string]any{"subscriber_id": "lead", "digests": []string{d2}, "commits": []map[string]string{{"sha": "bad"}}}, http.StatusBadRequest)
	require.Len(t, queue(), 1, "every refusal left it queued")
	call("mark-landed", map[string]any{"subscriber_id": "lead", "digests": []string{d2}, "commits": []map[string]string{{"sha": strings.Repeat("c", 40), "subject": "the fix"}}}, 200)
	call("mark-landed", map[string]any{"subscriber_id": "lead", "digests": []string{d2}}, 200) // idempotent
	require.Empty(t, queue(), "landed: out of the queue")
	r := postJSON(t, fmt.Sprintf("%s/api/board/mq/tasks/%d/mark-landed", server.URL, 99999), map[string]any{"subscriber_id": "lead", "digests": []string{d2}})
	defer r.Body.Close()
	require.Equal(t, http.StatusNotFound, r.StatusCode)
}
