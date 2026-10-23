package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
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
	mock    *mockSessionTerminal
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
	r := &codexRig{mock: mock, t: t, h: h, sid: sid, name: name, rollout: filepath.Join(dir, "rollout-2026-10-23T10-00-00-"+sid+".jsonl")}
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

// An open request closes only on an allow-list of events (#6672): its tool's PostToolUse, a turn's end (task_complete, turn_aborted) or a new turn (task_started, user_message). EVERY other rollout
// record leaves it open, however long after the request it is written: the kinds Codex was seen to write (Codex Test's captures), and kinds that do not exist yet. Before, any record outside
// event_msg counted as the turn going on and closed it, and a bookkeeping line 107 ms into the next second released a person's message into an open approval box (#6668).
var codexRecordsThatLeaveARequestOpen = [][2]string{
	{"response_item", "custom_tool_call"}, {"response_item", "custom_tool_call_output"}, {"response_item", "function_call"}, {"response_item", "function_call_output"},
	{"response_item", "message"}, {"response_item", "reasoning"}, {"response_item", "local_shell_call"}, {"response_item", "web_search_call"},
	{"event_msg", "item_completed"}, {"event_msg", "token_count"}, {"event_msg", "agent_message"}, {"event_msg", "agent_reasoning"}, {"event_msg", "exec_command_end"},
	{"token_usage_record", ""}, {"turn_context", ""}, {"session_meta", ""}, {"compacted", ""},
	{"response_item", "a_kind_codex_adds_next_year"}, {"event_msg", "an_event_kind_nobody_has_seen"}, {"a_top_level_kind_nobody_has_seen", "x"},
}

func TestNoOtherRolloutRecordClosesAnOpenCodexRequest(t *testing.T) {
	for _, after := range []time.Duration{1 * time.Second, 3 * time.Second, 30 * time.Second} {
		r := newCodexRig(t)
		r.openDialog()
		require.True(t, r.needsInput())
		for _, rec := range codexRecordsThatLeaveARequestOpen {
			r.line(rec[0], rec[1], after) // written this long after the request
			require.True(t, r.needsInput(), "%s %s at +%s: the request stays open", rec[0], rec[1], after)
		}
		assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid), "+%s: and a person's message is still held", after)
	}
}

// The events that close an open request: a turn's end or a new turn, written by Codex (a rollout record), each a whole second after it.
func TestTheEventsOnTheAllowListCloseAnOpenCodexRequest(t *testing.T) {
	for name, rec := range map[string][2]string{
		"task_complete (the turn ended)":       {"event_msg", "task_complete"},
		"turn_aborted (a refusal or Esc)":      {"event_msg", "turn_aborted"},
		"task_started (a new turn)":            {"event_msg", "task_started"},
		"user_message (the person's own turn)": {"event_msg", "user_message"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newCodexRig(t)
			r.openDialog()
			require.True(t, r.needsInput())
			r.line(rec[0], rec[1], 3*time.Second)
			assert.False(t, r.needsInput())
			assert.False(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid))
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
// hook follows. The sequence below is that one, with the dialog open for 1.5 s: the tail's records leave the request open, and the abort, the tail's end, closes it.
func TestACodexRealTimelineOpenDialogThenNoClosesOnlyAtTheAbort(t *testing.T) {
	r := newCodexRig(t)
	r.line("event_msg", "task_started", -3*time.Second)
	r.post(`{"event_type":"prompt_submit","summary":"User submitted prompt"}`)
	r.line("response_item", "custom_tool_call", -60*time.Millisecond) // recorded just before its dialog opens
	r.post(`{"event_type":"permission_request","summary":"Waiting for the user: permission (Bash)","tool_name":"Bash","detail_json":{"kind":"permission","tool_name":"Bash"}}`)
	require.True(t, r.needsInput())
	assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid))

	time.Sleep(1500 * time.Millisecond) // the dialog stays open and Codex writes nothing
	assert.True(t, r.needsInput(), "nothing written while the dialog is open: still open, even a second and a half on")

	// The person chooses No: the tool's output, bookkeeping and a message are written first.
	r.line("response_item", "custom_tool_call_output", 0)
	r.line("event_msg", "token_count", 0)
	r.line("response_item", "message", 0)
	assert.True(t, r.needsInput(), "none of the tail's records closes it: only the abort does")
	assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid))
	r.line("event_msg", "turn_aborted", 0)
	assert.False(t, r.needsInput(), "the abort closes it")
	assert.False(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid))
}

// #6658: a bookkeeping record (token_usage_record, turn_context) written well after the request, while the dialog is still open, does not close it: the row stays at Needs input and a person's
// message stays held; nor does a parallel tool's item_completed (Codex Test's #6657). (Before, any record outside event_msg counted as the turn going on.) Under the allow-list (#6672) no
// record of the turn closes it, the tool's own output included; the end of the turn does.
func TestALateBookkeepingRecordDoesNotCloseAnOpenCodexRequest(t *testing.T) {
	r := newCodexRig(t)
	r.openDialog()
	require.True(t, r.needsInput())
	for _, rec := range [][2]string{{"token_usage_record", ""}, {"turn_context", ""}, {"event_msg", "item_completed"}, {"response_item", "custom_tool_call_output"}} {
		r.line(rec[0], rec[1], 3*time.Second) // two seconds past the whole-second margin
		assert.True(t, r.needsInput(), rec[0]+" "+rec[1]+": the dialog is still open")
		assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid), rec[0]+" "+rec[1]+": and the message is still held")
	}
	r.line("event_msg", "task_complete", 3*time.Second)
	assert.False(t, r.needsInput(), "the turn's end closes it")
}

// lineAt appends one rollout record stamped at t exactly.
func (r *codexRig) lineAt(kind, payloadType string, at time.Time) {
	r.t.Helper()
	f, err := os.OpenFile(r.rollout, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(r.t, err)
	defer f.Close()
	_, err = f.WriteString(`{"timestamp":"` + at.UTC().Format(time.RFC3339Nano) + `","type":"` + kind + `","payload":{"type":"` + payloadType + `"}}` + "\n")
	require.NoError(r.t, err)
}

// #6668 on the 162 candidate (Codex Test's real run): the PermissionRequest at 19:56:44.895, a token_usage_record at 19:56:45.002 (107 ms later, in the NEXT whole second), and six seconds into the
// open approval a chat send was answered 200 and typed into the permission box. The sequence is replayed with the record 2 ms into the second after the request's: a person's send must be answered
// 202 held (with an id that cancels it), the row must stay at Needs input, and nothing is typed.
func TestABookkeepingRecordInTheNextSecondDoesNotLetASendIntoAnOpenCodexApproval(t *testing.T) {
	r := newCodexRig(t)
	term := &agentTerm{mockSessionTerminal: r.mock}
	r.h.terminal = term
	r.openDialog()
	require.True(t, r.needsInput())

	evs, err := r.h.ts.ListAgentEvents(context.Background(), "cx", 20, &r.sid)
	require.NoError(t, err)
	var requestAt time.Time
	for _, e := range evs {
		if e.EventType == "permission_request" {
			requestAt, err = time.Parse(time.RFC3339Nano, e.CreatedAt)
			require.NoError(t, err)
		}
	}
	require.False(t, requestAt.IsZero())
	nextSecond := requestAt.Truncate(time.Second).Add(time.Second + 2*time.Millisecond)
	r.lineAt("token_usage_record", "", nextSecond) // bookkeeping across the whole-second boundary, as Codex wrote it
	assert.True(t, r.needsInput(), "the row stays at Needs input: bookkeeping is not the turn going on")

	rt := chi.NewRouter()
	rt.Post("/api/sessions/live/{name}/send", r.h.Send)
	rt.Delete("/api/sessions/live/{name}/held/{id}", r.h.CancelHeld)
	do := func(method, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec
	}
	b, _ := json.Marshal(map[string]any{"command": "hello", "agent_type": "codex", "session_id": r.sid})
	rec := do(http.MethodPost, "/api/sessions/live/"+r.name+"/send", b)
	require.Equal(t, http.StatusAccepted, rec.Code, "held, not typed: "+rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	heldID, _ := out["held_id"].(string)
	require.NotEmpty(t, heldID)
	typed, _ := term.counts()
	assert.Empty(t, typed, "nothing reached the agent's input")

	rec = do(http.MethodDelete, "/api/sessions/live/"+r.name+"/held/"+heldID, nil)
	assert.Equal(t, http.StatusOK, rec.Code, "the held message cancels: "+rec.Body.String())
	assert.True(t, r.needsInput())
}
