package routes

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/naming"
)

// #3864: two teams on one Coral share role names. Everything a board types into terminals (a
// wait woken by a mention, a task assignment, a completion notice to the orchestrators, a post
// on the other board) reaches only that board's own sessions, by their exact session; a
// session of another board, a session that is no longer live, or a name that isn't an agent
// session is refused, with one log line per session and board.
func TestBoardNoticesReachOnlyThatBoardsSessions(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	term := newFGTerminal(newMockTerminal())
	h.terminal = term
	live := map[string]string{}
	h.SetLiveSessionLookup(func(_ context.Context, sid string) (string, bool, error) {
		agentType, ok := live[sid]
		return agentType, ok, nil
	})
	session := func(board, role, sid string) string {
		t.Helper()
		name := naming.SessionName("claude", sid)
		term.addSession(name, "/tmp/"+board)
		term.setFG(name, "node")
		live[sid] = "claude"
		_, err := h.bs.Subscribe(ctx, board, role, role, name, nil, "")
		require.NoError(t, err)
		return name
	}
	reviewerA := session("team-a", "Security Reviewer", "aaaaaaaa-0000-4000-8000-00000000000a")
	orchA := session("team-a", "Orchestrator", "aaaaaaaa-0000-4000-8000-0000000000a0")
	reviewerB := session("team-b", "Security Reviewer", "bbbbbbbb-0000-4000-8000-00000000000b")
	orchB := session("team-b", "Orchestrator", "bbbbbbbb-0000-4000-8000-0000000000b0")
	onlyB := []string{reviewerB, orchB}
	quietB := func(step string) {
		t.Helper()
		for _, name := range onlyB {
			assert.Empty(t, term.sentTo(name), "%s: team-b's %s got team-a's text", step, name)
		}
	}

	// A mention on team-a wakes team-a's reviewer's wait, not team-b's.
	for _, s := range []struct{ board, name string }{{"team-a", reviewerA}, {"team-b", reviewerB}} {
		_, err := h.bs.RegisterWait(ctx, s.board, "Security Reviewer", s.name, "message", "", "", time.Minute)
		require.NoError(t, err)
	}
	resp := postJSON(t, server.URL+"/api/board/team-a/messages", map[string]string{
		"subscriber_id": "Orchestrator", "content": "@Security Reviewer your open review: #3840"})
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Eventually(t, func() bool { return len(term.sentTo(reviewerA)) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Contains(t, term.sentTo(reviewerA)[0], "[Wait resolved]")
	w, err := h.bs.GetActiveWait(ctx, "team-b", "Security Reviewer")
	require.NoError(t, err)
	assert.NotNil(t, w, "team-b's wait is untouched")
	quietB("mention")

	// A task assigned to "Security Reviewer" on team-a.
	resp = postJSON(t, server.URL+"/api/board/team-a/tasks", map[string]string{
		"title": "Review #3849", "created_by": "Orchestrator", "subscriber_id": "Orchestrator", "assigned_to": "Security Reviewer"})
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.Eventually(t, func() bool { return len(term.sentTo(reviewerA)) == 2 }, 5*time.Second, 20*time.Millisecond)
	quietB("task assignment")

	// The completion notice goes to team-a's orchestrators.
	task, err := h.bs.CreateTask(ctx, "team-a", "Ship #3857", "", "medium", "Orchestrator")
	require.NoError(t, err)
	h.notifyOrchestratorsTaskCompleted(ctx, "team-a", "Security Reviewer", task, "approved")
	require.Len(t, term.sentTo(orchA), 1)
	assert.Contains(t, term.sentTo(orchA)[0], "completed by Security Reviewer")
	quietB("completion notice")

	// Typed into another board's session: refused, nothing typed, logged once.
	for i := 0; i < 2; i++ {
		err := h.sendAutomated(ctx, "team-a", reviewerB, "[Task #3857 completed by QA Engineer]")
		assert.True(t, errors.Is(err, errNoticeTarget), "err = %v", err)
	}
	quietB("direct send to another board's session")
	refusals := 0
	refusedNotices.Range(func(k, _ any) bool {
		if k.(string) == "team-a\x00"+reviewerB {
			refusals++
		}
		return true
	})
	assert.Equal(t, 1, refusals, "one log line per session and board")

	// A session that is no longer live, though still subscribed: refused, never another pane.
	delete(live, "aaaaaaaa-0000-4000-8000-00000000000a")
	err = h.sendAutomated(ctx, "team-a", reviewerA, "stale")
	assert.True(t, errors.Is(err, errNoticeTarget), "err = %v", err)
	assert.Len(t, term.sentTo(reviewerA), 2, "nothing more typed")
	live["aaaaaaaa-0000-4000-8000-00000000000a"] = "claude"

	// A subscription whose session name isn't an agent session.
	_, err = h.bs.Subscribe(ctx, "team-a", "cli-dev", "Dev", "cli-dev", nil, "")
	require.NoError(t, err)
	err = h.sendAutomated(ctx, "team-a", "cli-dev", "hello")
	assert.True(t, errors.Is(err, errNoticeTarget), "err = %v", err)

	// And the other way round: a post on team-b reaches team-b's reviewer only.
	before := len(term.sentTo(reviewerA))
	resp = postJSON(t, server.URL+"/api/board/team-b/messages", map[string]string{
		"subscriber_id": "Orchestrator", "content": "@Security Reviewer a question on our side"})
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Eventually(t, func() bool { return len(term.sentTo(reviewerB)) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Contains(t, term.sentTo(reviewerB)[0], "'team-b'")
	h.WaitBackground()
	assert.Len(t, term.sentTo(reviewerA), before, "team-a's reviewer got nothing from team-b")
}
