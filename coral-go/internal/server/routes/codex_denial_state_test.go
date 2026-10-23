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

// #6645: what clears a Codex's open approval request. Codex sends a PermissionRequest hook when its dialog opens and a PostToolUse when the approved tool ran; a refusal has no hook, so the
// agent's own rollout (its transcript) is the signal. Codex Test's real capture (#6646, Codex 0.163.0) shows that a refusal (the menu's No, or Esc) ABORTS the turn: it writes the tool's output,
// bookkeeping, a message and event_msg/turn_aborted, with no later hook, and there is no refusal that lets the turn go on. So the cases below are: the dialog open (nothing written), the abort
// tail, and any later record of the turn closing the request.
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

// Any record of the turn written a whole second after the request closes it: the first record of the tail a refusal writes is the tool's output; the others are what a turn that went on would write
// (a message, reasoning, another call), kept so a Codex that does continue after a refusal would not be held for.
func TestAnyLaterRolloutRecordClosesACodexRequest(t *testing.T) {
	for name, record := range map[string][2]string{
		"the tool's output (the first record of a refusal's tail)": {"response_item", "custom_tool_call_output"},
		"a function call's output":                                 {"response_item", "function_call_output"},
		"a model message":                                          {"response_item", "message"},
		"reasoning":                                                {"response_item", "reasoning"},
		"another tool call":                                        {"response_item", "function_call"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newCodexRig(t)
			r.openDialog()
			require.True(t, r.needsInput())
			r.line(record[0], record[1], 3*time.Second) // the turn went on, a whole second after the dialog opened
			assert.False(t, r.needsInput(), "seen from the turn's next record: no hook is needed")
			assert.False(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid), "so a person's message is not held for the rest of the turn")
		})
	}
}

// A refusal aborts the turn (the real No and Esc): turn_aborted alone, as the tail ends.
func TestACodexRefusalAbortsTheTurnAndClearsTheRequest(t *testing.T) {
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

// Codex Test's real timeline (#6646, Codex 0.163.0, trusted hooks): the call is recorded as response_item/custom_tool_call about 60 ms BEFORE the PermissionRequest hook; nothing at all is written
// to the rollout while the dialog is open (58 s observed); the menu's No (and Esc) end the turn: the tail is custom_tool_call_output, token_count, message, then event_msg/turn_aborted, and no
// hook follows. The sequence below is that one, with the dialog open for 1.5 s.
func TestACodexRealTimelineOpenDialogThenNoClearsOnlyAfterItsTail(t *testing.T) {
	r := newCodexRig(t)
	r.line("event_msg", "task_started", -3*time.Second)
	r.post(`{"event_type":"prompt_submit","summary":"User submitted prompt"}`)
	r.line("response_item", "custom_tool_call", -60*time.Millisecond) // recorded just before its dialog opens
	r.post(`{"event_type":"permission_request","summary":"Waiting for the user: permission (Bash)","tool_name":"Bash","detail_json":{"kind":"permission","tool_name":"Bash"}}`)
	require.True(t, r.needsInput())
	assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid))

	time.Sleep(1500 * time.Millisecond) // the dialog stays open and Codex writes nothing
	assert.True(t, r.needsInput(), "nothing written while the dialog is open: still open, even a second and a half on")
	assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid), "and a person's message is still held")

	// The person chooses No: the tool's output, bookkeeping, a message and the abort are written, newer than the request.
	r.line("response_item", "custom_tool_call_output", 0)
	assert.False(t, r.needsInput(), "the first record of the tail closes it")
	r.line("event_msg", "token_count", 0)
	r.line("response_item", "message", 0)
	r.line("event_msg", "turn_aborted", 0)
	assert.False(t, r.needsInput(), "and the abort keeps it closed")
	assert.False(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid))
}

// #6658: a bookkeeping record (token_usage_record, turn_context) written well after the request, while the dialog is still open, does not close it: the row stays at Needs input and a person's
// message stays held. (Before, any record outside event_msg counted as the turn going on.)
func TestALateBookkeepingRecordDoesNotCloseAnOpenCodexRequest(t *testing.T) {
	r := newCodexRig(t)
	r.openDialog()
	require.True(t, r.needsInput())
	for _, kind := range []string{"token_usage_record", "turn_context"} {
		r.line(kind, "", 3*time.Second) // two seconds past the whole-second margin
		assert.True(t, r.needsInput(), kind+": the dialog is still open")
		assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid), kind+": and the message is still held")
	}
	r.line("response_item", "custom_tool_call_output", 3*time.Second) // the turn's own record does close it
	assert.False(t, r.needsInput())
}
