package routes

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// #7082 failing-first regression, fixed by #7105.
// Task Queue audit messages are intentionally excluded from CheckUnread, so
// merely posting the next assignment cannot wake the worker via BoardNotifier.
func Test7082FreedWorkerGetsDeferredTaskNudge(t *testing.T) {
	for _, operation := range []string{"submit-review", "reject-review", "approve-review", "operator-complete"} {
		t.Run(operation, func(t *testing.T) {
			server, h := setupBoardTestServer(t)
			ctx := context.Background()
			const project = "wake7082"
			const worker = "Reviewer"
			const session = "claude-00000002-3864-4864-8864-000000000002"
			registerTaskPlanner(t, h, project, "Orchestrator")
			_, err := h.bs.Subscribe(ctx, project, worker, worker, session, nil, "mentions")
			require.NoError(t, err)
			terminal := newMockTerminal()
			terminal.addSession(session, "/tmp/fixture7082")
			h.SetTerminal(terminal)
			author := worker
			if operation == "reject-review" || operation == "approve-review" {
				author = "Author"
			}
			active, err := h.bs.CreateTask(ctx, project, "Active fixture", "", "medium", "Orchestrator", author)
			require.NoError(t, err)
			if operation == "reject-review" || operation == "approve-review" {
				_, err = h.bs.ClaimTask(ctx, project, author, active.ID)
				require.NoError(t, err)
				_, err = h.bs.SubmitCompletionReview(ctx, project, active.ID, author, "candidate", "success", "fixture", nil)
				require.NoError(t, err)
				_, _, err = h.bs.HandToOrchestrator(ctx, project, active.ID, author)
				require.NoError(t, err)
				_, reviews, err := h.bs.DelegateReview(ctx, project, active.ID, "Orchestrator", []string{worker})
				require.NoError(t, err)
				active = reviews[0]
			}
			_, err = h.bs.ClaimTask(ctx, project, worker, active.ID)
			require.NoError(t, err)
			next, err := h.bs.CreateTask(ctx, project, "Deferred fixture", "", "medium", "Orchestrator", worker)
			require.NoError(t, err)
			action := operation
			body := map[string]any{"subscriber_id": worker, "message": "fixture", "reason": "fixture"}
			if operation == "reject-review" || operation == "approve-review" {
				action = "reject"
				body["note"] = "fixture verdict"
				if operation == "approve-review" {
					action = "approve"
				}
			}
			if operation == "operator-complete" {
				action = "complete"
				body["subscriber_id"] = "Operator"
			}
			resp := postJSON(t, fmt.Sprintf("%s/api/board/%s/tasks/%d/%s", server.URL, project, active.ID, action), body)
			resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			h.WaitBackground()
			require.Nil(t, h.bs.ActiveTaskForSubscriber(ctx, project, worker))
			require.Equal(t, next.ID, h.bs.NextPendingTaskForSubscriber(ctx, project, worker).ID)
			// A direct wake is needed: audit-only next-task posts count as zero unread.
			unread, err := h.bs.CheckUnread(ctx, project, worker)
			require.NoError(t, err)
			require.Zero(t, unread)
			require.Len(t, terminal.sentTo(session), 1, "worker became idle with pending work but received no terminal wake")
			require.Contains(t, terminal.sentTo(session)[0], taskClaimNudge(next.ID))
		})
	}
}
