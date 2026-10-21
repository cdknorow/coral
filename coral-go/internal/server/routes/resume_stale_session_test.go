package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// TestResumeWithStaleSessionIDRefuses tests QA's bug bash repro:
// launch/register agent, restart (replaces old id with new id), then resume
// with the OLD id. The answer must be 409 session_not_live, and no duplicate agent is spawned.
func TestResumeWithStaleSessionIDRefuses(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	server, handler, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	oldSID := "11111111-1111-4111-8111-111111111111"
	name := naming.SessionName("claude", oldSID)
	dir := t.TempDir()
	term.addSession(name, dir)
	board := "coral-go-team"

	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  oldSID,
		AgentType:  "claude",
		AgentName:  "proj",
		WorkingDir: dir,
		BoardName:  &board,
	}))
	require.NoError(t, ss.SetDisplayName(ctx, oldSID, "Dev One"))
	_, err := handler.bs.Subscribe(ctx, board, "Dev One", "Dev One", name, nil, "all", true)
	require.NoError(t, err)

	// Step 1: POST /api/sessions/live/{name}/restart (replaces oldSID with newSID)
	restartBody, _ := json.Marshal(map[string]any{
		"agent_type": "claude",
		"session_id": oldSID,
	})
	resp, err := http.Post(server.URL+"/api/sessions/live/"+name+"/restart", "application/json", bytes.NewReader(restartBody))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var restartOut struct {
		SessionID   string `json:"session_id"`
		SessionName string `json:"session_name"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&restartOut))
	newSID := restartOut.SessionID
	require.NotEmpty(t, newSID)
	require.NotEqual(t, oldSID, newSID)

	// Step 2: POST /api/sessions/live/{name}/resume with the OLD id (oldSID)
	resumeStaleBody, _ := json.Marshal(map[string]any{
		"agent_type":         "claude",
		"session_id":         "99999999-9999-4999-8999-999999999999",
		"current_session_id": oldSID,
	})
	respStale, err := http.Post(server.URL+"/api/sessions/live/"+restartOut.SessionName+"/resume", "application/json", bytes.NewReader(resumeStaleBody))
	require.NoError(t, err)
	defer respStale.Body.Close()

	var staleErrOut map[string]any
	require.NoError(t, json.NewDecoder(respStale.Body).Decode(&staleErrOut))
	assert.Equal(t, http.StatusConflict, respStale.StatusCode, "stale session_id resume returns 409")
	assert.Equal(t, "session_not_live", staleErrOut["code"])
	assert.Equal(t, "session is not live", staleErrOut["error"])

	// Also verify that passing oldSID as {name} without current_session_id is refused
	resumeStaleURL, _ := json.Marshal(map[string]any{
		"agent_type": "claude",
		"session_id": "99999999-9999-4999-8999-999999999999",
	})
	respStaleURL, err := http.Post(server.URL+"/api/sessions/live/"+name+"/resume", "application/json", bytes.NewReader(resumeStaleURL))
	require.NoError(t, err)
	defer respStaleURL.Body.Close()
	assert.Equal(t, http.StatusConflict, respStaleURL.StatusCode, "stale session name resume returns 409")

	// Step 3: Verify no duplicate agent exists
	liveSessions, err := ss.GetAllLiveSessions(ctx)
	require.NoError(t, err)
	require.Len(t, liveSessions, 1, "exactly one live session remains (no duplicate spawned)")
	assert.Equal(t, newSID, liveSessions[0].SessionID)

	// Step 4: Resume with the CURRENT id (newSID) works
	resumeCurBody, _ := json.Marshal(map[string]any{
		"agent_type":         "claude",
		"session_id":         "88888888-8888-4888-8888-888888888888",
		"current_session_id": newSID,
	})
	respCur, err := http.Post(server.URL+"/api/sessions/live/"+restartOut.SessionName+"/resume", "application/json", bytes.NewReader(resumeCurBody))
	require.NoError(t, err)
	defer respCur.Body.Close()
	require.Equal(t, http.StatusOK, respCur.StatusCode, "resume with current session id succeeds")

	var curOut struct {
		SessionID string `json:"session_id"`
	}
	require.NoError(t, json.NewDecoder(respCur.Body).Decode(&curOut))
	require.NotEmpty(t, curOut.SessionID)

	liveAfter, err := ss.GetAllLiveSessions(ctx)
	require.NoError(t, err)
	require.Len(t, liveAfter, 1, "still only one live session (replaces current)")
	assert.Equal(t, curOut.SessionID, liveAfter[0].SessionID)
}

// TestResumeSleepingSessionSucceeds verifies that resuming a genuinely sleeping
// session wakes it up, replaces the session, and retains identity.
func TestResumeSleepingSessionSucceeds(t *testing.T) {
	server, handler, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "22222222-2222-4222-8222-222222222222"
	name := naming.SessionName("claude", sid)
	dir := t.TempDir()
	board := "coral-go-team"

	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  sid,
		AgentType:  "claude",
		AgentName:  "proj",
		WorkingDir: dir,
		BoardName:  &board,
	}))
	require.NoError(t, ss.SetDisplayName(ctx, sid, "Sleeping Dev"))
	require.NoError(t, ss.SetSessionSleeping(ctx, sid, true))
	_, err := handler.bs.Subscribe(ctx, board, "Sleeping Dev", "Sleeping Dev", name, nil, "all", true)
	require.NoError(t, err)

	// In sleeping state, terminal session is gone
	term.mu.Lock()
	delete(term.sessions, name)
	term.mu.Unlock()

	body, _ := json.Marshal(map[string]any{
		"agent_type":         "claude",
		"session_id":         "77777777-7777-4777-8777-777777777777",
		"current_session_id": sid,
	})
	resp, err := http.Post(server.URL+"/api/sessions/live/"+name+"/resume", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "resuming a genuinely sleeping session succeeds")

	var out struct {
		SessionID   string `json:"session_id"`
		SessionName string `json:"session_name"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotEmpty(t, out.SessionID)

	ls, err := ss.GetLiveSession(ctx, out.SessionID)
	require.NoError(t, err)
	require.NotNil(t, ls)
	assert.Equal(t, 0, ls.IsSleeping, "resumed session is awake (is_sleeping=0)")

	dn, err := ss.GetDisplayName(ctx, out.SessionID)
	require.NoError(t, err)
	require.NotNil(t, dn)
	assert.Equal(t, "Sleeping Dev", *dn)
}

// TestResumeUnknownSessionAnswers404 verifies unknown session answers 404.
func TestResumeUnknownSessionAnswers404(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)

	body, _ := json.Marshal(map[string]any{
		"agent_type":         "claude",
		"session_id":         "77777777-7777-4777-8777-777777777777",
		"current_session_id": "00000000-0000-4000-8000-000000000000",
	})
	resp, err := http.Post(server.URL+"/api/sessions/live/team/resume", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, "session_not_found", out["code"])
}
