package routes

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// #6645: what clears a Codex's open approval request. Codex sends a PermissionRequest hook when its dialog opens and a PostToolUse when the approved tool ran; a denial has no hook, so the
// agent's own rollout (its transcript) is the signal: a later record of the turn (a tool's output, a message, reasoning) means the turn went on, and a turn_aborted means it was cancelled.
// The rollout record types here are the ones Coral already reads (event_msg task_started and turn_aborted; response_item for the rest); the timeline is built to the order Codex Test's real
// capture shows (a call is recorded before its dialog opens, nothing is written while the dialog is open).
type codexRig struct {
	t       *testing.T
	h       *SessionsHandler
	sid     string
	name    string
	rollout string
	post    func(body string)
}

func newCodexRig(t *testing.T) *codexRig {
	t.Helper()
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	server, h, mock, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "cccccccc-6645-4000-8000-000000000001"
	name := naming.SessionName("codex", sid)
	mock.addSession(name, "/tmp/project")
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "codex", AgentName: "cx", WorkingDir: "/tmp/project"}))
	tok := agentToken(t, h, sid, name)
	dir := filepath.Join(codexHome, "sessions", "2026", "10", "23")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	r := &codexRig{t: t, h: h, sid: sid, name: name, rollout: filepath.Join(dir, "rollout-2026-10-23T10-00-00-"+sid+".jsonl")}
	r.post = func(body string) {
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/cx/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.SessionTokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
	}
	return r
}

// line appends one rollout record at offset from now.
func (r *codexRig) line(kind, payloadType string, offset time.Duration) {
	r.t.Helper()
	f, err := os.OpenFile(r.rollout, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(r.t, err)
	defer f.Close()
	ts := time.Now().UTC().Add(offset).Format(time.RFC3339Nano)
	_, err = f.WriteString(`{"timestamp":"` + ts + `","type":"` + kind + `","payload":{"type":"` + payloadType + `"}}` + "\n")
	require.NoError(r.t, err)
}

func (r *codexRig) needsInput() bool {
	return r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput
}

// The turn starts, its call is recorded, the dialog opens (the hook says so).
func (r *codexRig) openDialog() {
	r.t.Helper()
	r.line("event_msg", "task_started", -3*time.Second)
	r.line("response_item", "function_call", -2*time.Second)
	r.post(`{"event_type":"prompt_submit","summary":"User submitted prompt"}`)
	r.post(`{"event_type":"permission_request","summary":"Waiting for the user: permission (Bash)","tool_name":"Bash","detail_json":{"kind":"permission","tool_name":"Bash"}}`)
}

func TestACodexApprovalStaysOpenWhileTheDialogIsOpen(t *testing.T) {
	r := newCodexRig(t)
	r.openDialog()
	assert.True(t, r.needsInput(), "the dialog is open")
	assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid))
	r.line("event_msg", "token_count", 1*time.Second) // bookkeeping the CLI writes: not the turn going on
	assert.True(t, r.needsInput(), "nothing of the turn after the request: still open")
	time.Sleep(1200 * time.Millisecond)
	assert.True(t, r.needsInput(), "and it stays open as long as nothing is written")
}

func TestACodexDenialClearsTheRequestWhenTheTurnGoesOn(t *testing.T) {
	for name, record := range map[string][2]string{
		"the tool's output (denied)": {"response_item", "function_call_output"},
		"a model message":            {"response_item", "message"},
		"reasoning":                  {"response_item", "reasoning"},
		"another tool call":          {"response_item", "function_call"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newCodexRig(t)
			r.openDialog()
			require.True(t, r.needsInput())
			r.line(record[0], record[1], 3*time.Second) // the turn went on, a whole second after the dialog opened
			assert.False(t, r.needsInput(), "the denial is seen from the turn's next record: no hook is needed")
			assert.False(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid), "so a person's message is not held for the rest of the turn")
		})
	}
}

func TestACancelledCodexApprovalClearsTheRequest(t *testing.T) {
	r := newCodexRig(t)
	r.openDialog()
	require.True(t, r.needsInput())
	r.line("event_msg", "turn_aborted", 3*time.Second)
	assert.False(t, r.needsInput())
	assert.False(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid))
}

func TestACodexApprovalIsClearedByItsToolsOwnPostToolUse(t *testing.T) {
	r := newCodexRig(t)
	r.openDialog()
	require.True(t, r.needsInput())
	r.post(`{"event_type":"tool_use","summary":"Ran: ls","tool_name":"Bash"}`)
	assert.False(t, r.needsInput(), "approved: the tool ran")
}
