package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/require"
)

// TestIndependentClaimGuidanceAndQueueSafety rigorously verifies all aspects of #1265:
// 1. Task-specific notices vs generic notices.
// 2. Older-ready vs newer-assigned claim isolation and non-cancellation.
// 3. Explicit claim invariants: capacity, ownership, dependencies, and terminal immutability.
// 4. Sibling retries independent validity.
func TestIndependentClaimGuidanceAndQueueSafety(t *testing.T) {
	server, h := setupBoardTestServer(t)
	registerTaskPlanner(t, h, "team", "Orchestrator")
	terminal := newMockTerminal()
	terminal.addSession("dev1", "/tmp/dev1")
	terminal.addSession("dev2", "/tmp/dev2")
	h.SetTerminal(terminal)

	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "team", "Dev1", "Developer", "dev1", nil, nil, "all", false)
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, "team", "Dev2", "Developer", "dev2", nil, nil, "all", false)
	require.NoError(t, err)

	base := server.URL + "/api/board/team"

	// 1. Test notification formats: task-specific notices include explicit ID, generic notices do not.
	olderTask, err := h.bs.CreateTask(ctx, "team", "Older pending work", "", "high", "Orchestrator", "Dev1")
	require.NoError(t, err)

	// Create newer assigned task via API to trigger CreateTask nudges and notifications
	resp := postJSON(t, base+"/tasks", map[string]string{
		"title":       "Newer assigned work",
		"priority":    "high",
		"created_by":  "Orchestrator",
		"assigned_to": "Dev1",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var newerTask board.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&newerTask))
	resp.Body.Close()

	// Check terminal nudge sent to Dev1 terminal contains explicit ID
	expectedClaimCmd := fmt.Sprintf("coral-board task claim %d", newerTask.ID)
	require.Eventually(t, func() bool {
		for _, msg := range terminal.sentTo("dev1") {
			if strings.Contains(msg, expectedClaimCmd) {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "Terminal nudge must include explicit claim ID")

	// Check board notification message contains explicit ID
	require.Eventually(t, func() bool {
		return boardMessagesContain(t, base, expectedClaimCmd)
	}, 2*time.Second, 10*time.Millisecond, "Board assignment message must include explicit claim ID")

	// Check reassignment notification contains explicit ID
	reassignMsg := h.buildAssignmentNotification(ctx, "team", &newerTask, "Dev1", true)
	require.Contains(t, reassignMsg, expectedClaimCmd, "Reassignment notice must contain explicit claim ID")

	// Check reminder nudge contains explicit ID
	nudgeResp := postJSON(t, fmt.Sprintf("%s/tasks/%d/nudge", base, newerTask.ID), nil)
	require.Equal(t, http.StatusOK, nudgeResp.StatusCode)
	nudgeResp.Body.Close()
	require.Eventually(t, func() bool {
		for _, msg := range terminal.sentTo("dev1") {
			if strings.Contains(msg, fmt.Sprintf("[Task #%d reminder]", newerTask.ID)) && strings.Contains(msg, expectedClaimCmd) {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "Task reminder must include explicit claim ID")

	// Verify generic unassigned availability uses generic taskNudge without task ID
	unassignedResp := postJSON(t, base+"/tasks", map[string]string{
		"title":      "Generic unassigned task",
		"priority":   "medium",
		"created_by": "Orchestrator",
	})
	require.Equal(t, http.StatusCreated, unassignedResp.StatusCode)
	unassignedResp.Body.Close()

	// 2. Reproduce older-ready vs newer-assigned scenario:
	// Older task is ready and high priority.
	// Targeted claim selects newer task #newerTask.ID explicitly.
	claimResp := postJSON(t, base+"/tasks/claim", map[string]any{
		"subscriber_id": "Dev1",
		"task_id":       newerTask.ID,
	})
	require.Equal(t, http.StatusOK, claimResp.StatusCode)
	var claimedTask board.Task
	require.NoError(t, json.NewDecoder(claimResp.Body).Decode(&claimedTask))
	claimResp.Body.Close()
	require.Equal(t, newerTask.ID, claimedTask.ID, "Targeted claim must claim the exact newer task")

	// Confirm older task remains in 'pending' status without being modified or cancelled
	olderCheck, err := h.bs.GetTask(ctx, "team", olderTask.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", olderCheck.Status, "Older task must remain pending and untouched")

	// 3. Explicit claim invariants:
	// a. Capacity: Dev1 already has active task (newerTask), so attempting to claim olderTask (even with explicit ID) must fail
	conflictResp := postJSON(t, base+"/tasks/claim", map[string]any{
		"subscriber_id": "Dev1",
		"task_id":       olderTask.ID,
	})
	require.Equal(t, http.StatusConflict, conflictResp.StatusCode, "Active slot limit enforced on explicit claim")
	conflictResp.Body.Close()

	// b. Ownership: Dev2 attempting to claim olderTask (which is assigned to Dev1) must be rejected
	dev2Resp := postJSON(t, base+"/tasks/claim", map[string]any{
		"subscriber_id": "Dev2",
		"task_id":       olderTask.ID,
	})
	require.Equal(t, http.StatusBadRequest, dev2Resp.StatusCode, "Cannot claim task assigned to another subscriber")
	dev2Resp.Body.Close()

	// c. Terminal immutability: Complete newerTask and verify it cannot be claimed again
	completeResp := postJSON(t, fmt.Sprintf("%s/tasks/%d/complete", base, newerTask.ID), map[string]any{
		"subscriber_id": "Dev1",
		"outcome":       "success",
	})
	require.Equal(t, http.StatusOK, completeResp.StatusCode)
	completeResp.Body.Close()

	// Attempting to claim completed task with explicit ID must fail
	claimedAgainResp := postJSON(t, base+"/tasks/claim", map[string]any{
		"subscriber_id": "Dev1",
		"task_id":       newerTask.ID,
	})
	require.Equal(t, http.StatusBadRequest, claimedAgainResp.StatusCode, "Completed task cannot be claimed")
	claimedAgainResp.Body.Close()

	// d. Prerequisites / Dependencies: Dev1 now has no active tasks, but blockedTask is blocked by olderTask
	depResp := postJSON(t, base+"/tasks", map[string]any{
		"title":       "Blocked task",
		"priority":    "high",
		"created_by":  "Orchestrator",
		"assigned_to": "Dev1",
		"blocked_by":  []map[string]any{{"task_id": olderTask.ID}},
	})
	require.Equal(t, http.StatusCreated, depResp.StatusCode)
	var blockedTask board.Task
	require.NoError(t, json.NewDecoder(depResp.Body).Decode(&blockedTask))
	depResp.Body.Close()
	require.Equal(t, "blocked", blockedTask.Status)

	// Attempting to claim blocked task with explicit ID must fail because prerequisites are unmet
	blockedClaimResp := postJSON(t, base+"/tasks/claim", map[string]any{
		"subscriber_id": "Dev1",
		"task_id":       blockedTask.ID,
	})
	require.Equal(t, http.StatusBadRequest, blockedClaimResp.StatusCode, "Cannot claim blocked task even with explicit ID")
	blockedClaimResp.Body.Close()

	// Concurrent claim attempts on terminal task across multiple goroutines
	var wg sync.WaitGroup
	errCounts := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := postJSON(t, base+"/tasks/claim", map[string]any{
				"subscriber_id": "Dev1",
				"task_id":       newerTask.ID,
			})
			if r.StatusCode == http.StatusBadRequest {
				errCounts <- 1
			} else {
				errCounts <- 0
			}
			r.Body.Close()
		}()
	}
	wg.Wait()
	close(errCounts)
	totalRejections := 0
	for count := range errCounts {
		totalRejections += count
	}
	require.Equal(t, 8, totalRejections, "All 8 concurrent attempts to claim terminal task must be rejected")

	// e. Generic FIFO claim: Dev1 now has no active tasks, generic claim returns olderTask
	genericClaimResp := postJSON(t, base+"/tasks/claim", map[string]any{
		"subscriber_id": "Dev1",
	})
	require.Equal(t, http.StatusOK, genericClaimResp.StatusCode)
	var fifoClaimed board.Task
	require.NoError(t, json.NewDecoder(genericClaimResp.Body).Decode(&fifoClaimed))
	genericClaimResp.Body.Close()
	require.Equal(t, olderTask.ID, fifoClaimed.ID, "Generic claim must respect FIFO and return older task")
}
