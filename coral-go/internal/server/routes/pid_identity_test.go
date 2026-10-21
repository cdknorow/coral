package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// livePIDBackend reports each session's process as its terminal backend sees it now.
type livePIDBackend struct {
	ptymanager.TerminalBackend
	live map[string]int
}

func (b livePIDBackend) SessionPID(_ context.Context, name string) (int, error) {
	return b.live[name], nil
}

// #3864: coral-board falls back to its process tree when it can't find its state file, and
// the server named the session by any ancestor pid recorded for an active session. A
// recorded pid outlives its process; once the number is reused in another agent's process
// tree, that agent was handed this session's board and role (two teams' "Security
// Reviewer"s). Only a session's live process counts, the nearest ancestor wins, and two
// sessions on one pid give no answer.
func TestResolveByPIDsTrustsOnlyLiveProcesses(t *testing.T) {
	_, handler, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const (
		goReviewer  = "11111111-1111-4111-8111-111111111111"
		hubReviewer = "22222222-2222-4222-8222-222222222222"
		goLead      = "33333333-3333-4333-8333-333333333333"
		twinA       = "44444444-4444-4444-8444-444444444444"
		twinB       = "55555555-5555-4555-8555-555555555555"
	)
	live := map[string]int{}
	register := func(sid, board, role string, recorded, now int) {
		t.Helper()
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "repo",
			WorkingDir: "/tmp/repo", DisplayName: &role, BoardName: &board}))
		require.NoError(t, ss.UpdateSessionPID(ctx, sid, recorded))
		live[naming.SessionName("claude", sid)] = now
	}
	register(goReviewer, "coral-go-team", "Security Reviewer", 5001, 5001)
	register(hubReviewer, "coral-hub", "Security Reviewer", 7001, 7999) // its recorded pid is stale
	register(goLead, "coral-go-team", "Lead Developer", 6001, 6001)
	register(twinA, "team-x", "Dev", 4001, 4001) // two sessions claiming one live pid
	register(twinB, "team-y", "Dev", 4001, 4001)
	handler.backend = livePIDBackend{live: live}

	resolve := func(chain ...int) (int, map[string]string) {
		t.Helper()
		parts := make([]string, len(chain))
		for i, p := range chain {
			parts[i] = strconv.Itoa(p)
		}
		rec := httptest.NewRecorder()
		handler.ResolveByPIDs(rec, httptest.NewRequest("GET", "/api/sessions/resolve?pids="+strings.Join(parts, ","), nil))
		var out map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// The incident's shape: 7001 (the stale pid recorded for coral-hub's reviewer) is now a
	// process in another agent's tree. No answer, never coral-hub's board and role.
	code, out := resolve(9000, 7001, 100)
	assert.Equal(t, http.StatusNotFound, code, "a reused pid names nobody: %v", out)

	// The caller's own session, by its live process.
	code, out = resolve(9001, 5001, 100)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "coral-go-team", out["project"])
	assert.Equal(t, "Security Reviewer", out["subscriber_id"])
	assert.Equal(t, naming.SessionName("claude", goReviewer), out["session_name"])

	// The nearest live ancestor wins over a farther one.
	code, out = resolve(9002, 5001, 6001)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, naming.SessionName("claude", goReviewer), out["session_name"])
	code, out = resolve(9003, 6001, 5001)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, naming.SessionName("claude", goLead), out["session_name"])

	// A stale pid nearer than the caller's own session is skipped, not trusted.
	code, out = resolve(7001, 5001)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "coral-go-team", out["project"])

	// Two sessions on one pid: no guess.
	code, _ = resolve(9004, 4001, 100)
	assert.Equal(t, http.StatusNotFound, code)

	// A backend that can't say which process a session runs is no verification.
	handler.backend = livePIDBackend{live: map[string]int{}}
	code, _ = resolve(9001, 5001)
	assert.Equal(t, http.StatusNotFound, code)
}
