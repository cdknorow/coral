package routes

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexTranscriptStateHTTPWebSocketAndSingleSession(t *testing.T) {
	server, h, terminal, ss := setupSessionsTestServer(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	dir := filepath.Join(home, "sessions", "2026", "09", "26")
	require.NoError(t, os.MkdirAll(dir, 0700))
	sid := uuid.NewString()
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentName: "lead", AgentType: "codex", WorkingDir: "/tmp"}))
	terminal.addSession("codex-"+sid, "/tmp")
	path := filepath.Join(dir, "rollout-"+sid+".jsonl")
	data := ""
	for _, step := range []struct {
		event             string
		working, awaiting bool
	}{
		{"task_started", true, false},
		{"task_complete", false, true},
		{"task_started", true, false},
		{"turn_aborted", false, false},
	} {
		data += `{"timestamp":"2026-09-26T17:00:00Z","type":"event_msg","payload":{"type":"` + step.event + `"}}` + "\n"
		require.NoError(t, os.WriteFile(path, []byte(data), 0600))
		httpState, wsState := httpAndWSState(t, server, h, sid)
		require.Equal(t, httpState, wsState)
		require.Equal(t, step.working, httpState["working"], step.event)
		require.Equal(t, step.awaiting, httpState["awaiting_user"], step.event)
		single := h.deriveSingleSessionState(context.Background(), sid, 3600)
		require.Equal(t, step.working, single.Working, "silent long turn must match list")
		require.Equal(t, step.awaiting, single.AwaitingUser)
	}
}
