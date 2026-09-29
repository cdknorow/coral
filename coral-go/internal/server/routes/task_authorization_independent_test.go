package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTaskAuthorization_ComprehensiveIndependentVerification exercises all
// aspects of shared task creation and reassignment authorization:
// 1. Operator creation (with created_by, subscriber_id, or both)
// 2. Orchestrator creation (job_title=Orchestrator or can_peek=1)
// 3. Worker denial (create, reassign, PATCH assigned_to)
// 4. Mismatched actor spoofing (created_by != subscriber_id)
// 5. Worker title-only edit allowed without planner check
// 6. Cross-board and inactive registration denial
// 7. Preserved worker claim and completion
func TestTaskAuthorization_ComprehensiveIndependentVerification(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()

	// Setup board subscribers
	// 1. Registered Orchestrator on "main-board"
	_, err := h.bs.Subscribe(ctx, "main-board", "lead-orch", "Orchestrator", "", nil, nil, "all", true)
	require.NoError(t, err)

	// 2. Custom title with can_peek=true on "main-board"
	_, err = h.bs.Subscribe(ctx, "main-board", "peek-orch", "Team Lead", "", nil, nil, "all", true)
	require.NoError(t, err)

	// 3. Regular worker on "main-board"
	_, err = h.bs.Subscribe(ctx, "main-board", "dev-worker", "Backend Dev", "", nil, nil, "all", false)
	require.NoError(t, err)

	// 4. Inactive subscriber on "main-board"
	_, err = h.bs.Subscribe(ctx, "main-board", "ex-orch", "Orchestrator", "", nil, nil, "all", true)
	require.NoError(t, err)
	_, err = h.bs.Unsubscribe(ctx, "main-board", "ex-orch")
	require.NoError(t, err)

	// 5. Orchestrator on another board ("other-board")
	_, err = h.bs.Subscribe(ctx, "other-board", "foreign-orch", "Orchestrator", "", nil, nil, "all", true)
	require.NoError(t, err)

	boardURL := server.URL + "/api/board/main-board/tasks"

	// ─── 1. Operator Creation and Assignment ────────────────────────────
	t.Run("Operator_Create_With_CreatedBy", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"created_by": "Operator",
			"title":      "Task by Operator created_by",
		})
		require.Equal(t, http.StatusCreated, r.StatusCode)
		r.Body.Close()
	})

	t.Run("Operator_Create_With_SubscriberID", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"subscriber_id": "Operator",
			"title":         "Task by Operator subscriber_id",
		})
		require.Equal(t, http.StatusCreated, r.StatusCode)
		r.Body.Close()
	})

	t.Run("Operator_Create_With_Both_Matching", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"created_by":    "Operator",
			"subscriber_id": "Operator",
			"title":         "Task by Operator both matching",
		})
		require.Equal(t, http.StatusCreated, r.StatusCode)
		r.Body.Close()
	})

	// ─── 2. Registered Orchestrator & CanPeek Creation ─────────────────
	var sharedTaskID int64
	t.Run("Orchestrator_Create_Allowed", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"subscriber_id": "lead-orch",
			"title":         "Shared Task 1",
			"assigned_to":   "dev-worker",
		})
		require.Equal(t, http.StatusCreated, r.StatusCode)
		var task struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&task))
		r.Body.Close()
		sharedTaskID = task.ID
	})

	t.Run("CanPeek_Create_Allowed", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"subscriber_id": "peek-orch",
			"title":         "Shared Task 2 by Peek Orch",
		})
		require.Equal(t, http.StatusCreated, r.StatusCode)
		r.Body.Close()
	})

	// ─── 3. Worker Denial on Creation ──────────────────────────────────
	t.Run("Worker_Create_Denied", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"subscriber_id": "dev-worker",
			"title":         "Unauthorized Worker Task",
		})
		require.Equal(t, http.StatusForbidden, r.StatusCode)
		r.Body.Close()
	})

	t.Run("Worker_With_Forged_Body_Attributes_Denied", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"subscriber_id": "dev-worker",
			"created_by":    "dev-worker",
			"job_title":     "Orchestrator",
			"can_peek":      1,
			"title":         "Forged Worker Task",
		})
		require.Equal(t, http.StatusForbidden, r.StatusCode)
		r.Body.Close()
	})

	// ─── 4. Precedence & Spoofing Mismatches ───────────────────────────
	t.Run("Worker_Spoofing_Operator_In_CreatedBy_Denied", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"subscriber_id": "dev-worker",
			"created_by":    "Operator",
			"title":         "Spoof Attempt 1",
		})
		require.Equal(t, http.StatusForbidden, r.StatusCode)
		r.Body.Close()
	})

	t.Run("Worker_Spoofing_Orchestrator_In_CreatedBy_Denied", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"subscriber_id": "dev-worker",
			"created_by":    "lead-orch",
			"title":         "Spoof Attempt 2",
		})
		require.Equal(t, http.StatusForbidden, r.StatusCode)
		r.Body.Close()
	})

	// ─── 5. Cross-Board & Inactive Registration Denial ─────────────────
	t.Run("Foreign_Orchestrator_CrossBoard_Denied", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"subscriber_id": "foreign-orch",
			"title":         "Cross Board Task",
		})
		require.Equal(t, http.StatusForbidden, r.StatusCode)
		r.Body.Close()
	})

	t.Run("Inactive_Orchestrator_Denied", func(t *testing.T) {
		r := postJSON(t, boardURL, map[string]any{
			"subscriber_id": "ex-orch",
			"title":         "Inactive Orch Task",
		})
		require.Equal(t, http.StatusForbidden, r.StatusCode)
		r.Body.Close()
	})

	// ─── 6. Reassignment & PATCH assigned_to Checks ────────────────────
	require.NotZero(t, sharedTaskID)

	t.Run("Worker_Reassign_Denied", func(t *testing.T) {
		r := postJSON(t, fmt.Sprintf("%s/%d/reassign", boardURL, sharedTaskID), map[string]any{
			"subscriber_id": "dev-worker",
			"assignee":      "peek-orch",
		})
		require.Equal(t, http.StatusForbidden, r.StatusCode)
		r.Body.Close()
	})

	t.Run("Worker_PATCH_AssignedTo_Denied", func(t *testing.T) {
		b, _ := json.Marshal(map[string]any{
			"subscriber_id": "dev-worker",
			"assigned_to":   "peek-orch",
		})
		req, err := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/%d", boardURL, sharedTaskID), bytes.NewReader(b))
		require.NoError(t, err)
		r, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, r.StatusCode)
		r.Body.Close()
	})

	t.Run("Worker_PATCH_NonAssignmentField_Allowed", func(t *testing.T) {
		b, _ := json.Marshal(map[string]any{
			"subscriber_id": "dev-worker",
			"title":         "Updated Title by Worker",
		})
		req, err := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/%d", boardURL, sharedTaskID), bytes.NewReader(b))
		require.NoError(t, err)
		r, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, r.StatusCode)
		r.Body.Close()

		saved, err := h.bs.GetTask(ctx, "main-board", sharedTaskID)
		require.NoError(t, err)
		assert.Equal(t, "Updated Title by Worker", saved.Title)
		assert.Equal(t, "dev-worker", *saved.AssignedTo)
	})

	t.Run("Operator_Reassign_Allowed", func(t *testing.T) {
		r := postJSON(t, fmt.Sprintf("%s/%d/reassign", boardURL, sharedTaskID), map[string]any{
			"subscriber_id": "Operator",
			"assignee":      "peek-orch",
		})
		require.Equal(t, http.StatusOK, r.StatusCode)
		r.Body.Close()

		saved, err := h.bs.GetTask(ctx, "main-board", sharedTaskID)
		require.NoError(t, err)
		assert.Equal(t, "peek-orch", *saved.AssignedTo)
	})

	t.Run("Orchestrator_PATCH_AssignedTo_Allowed", func(t *testing.T) {
		b, _ := json.Marshal(map[string]any{
			"subscriber_id": "lead-orch",
			"assigned_to":   "dev-worker",
		})
		req, err := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/%d", boardURL, sharedTaskID), bytes.NewReader(b))
		require.NoError(t, err)
		r, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, r.StatusCode)
		r.Body.Close()

		saved, err := h.bs.GetTask(ctx, "main-board", sharedTaskID)
		require.NoError(t, err)
		assert.Equal(t, "dev-worker", *saved.AssignedTo)
	})

	// ─── 7. Preserved Worker Claim and Complete ────────────────────────
	t.Run("Worker_Claim_And_Complete_Preserved", func(t *testing.T) {
		r := postJSON(t, boardURL+"/claim", map[string]any{
			"subscriber_id": "dev-worker",
			"task_id":       sharedTaskID,
		})
		require.Equal(t, http.StatusOK, r.StatusCode)
		r.Body.Close()

		r = postJSON(t, fmt.Sprintf("%s/%d/complete", boardURL, sharedTaskID), map[string]any{
			"subscriber_id": "dev-worker",
			"message":       "completed successfully",
		})
		require.Equal(t, http.StatusOK, r.StatusCode)
		r.Body.Close()

		saved, err := h.bs.GetTask(ctx, "main-board", sharedTaskID)
		require.NoError(t, err)
		assert.Equal(t, "completed", saved.Status)
		assert.Equal(t, "dev-worker", *saved.CompletedBy)
	})
}
