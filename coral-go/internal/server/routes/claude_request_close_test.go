package routes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6687: what closes an open Claude permission request, and what leaves it open, so a person's chat message stays held behind it (#6610).

// Claude's transcript can close a request through two records only: a system turn_duration (the turn ended) and the user text "[Request interrupted by user ...]" (a refusal or an interrupt). The
// reader returns nothing else (jsonl/claude_turn.go), so any other record, however late, leaves it open. The rig writes the CLI's other record kinds at +1 s, +3 s and +30 s.
func claudeTranscript(t *testing.T, r *holdRig) func(line string) {
	t.Helper()
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	// The session's folder is /tmp/project; on a Mac /tmp is /private/tmp, and the transcript folder is named from whichever the reader resolves: both are written.
	var paths []string
	for _, folder := range []string{"-tmp-project", "-private-tmp-project"} {
		dir := filepath.Join(projects, folder)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		paths = append(paths, filepath.Join(dir, r.sid+".jsonl"))
	}
	write := func(line string) {
		for _, path := range paths {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			require.NoError(t, err)
			_, err = f.WriteString(line + "\n")
			require.NoError(t, err)
			f.Close()
		}
	}
	// The session's transcript exists from its first prompt (the reader treats a file it finds already there as the live one).
	write(`{"type":"user","timestamp":"` + rolloutStamp(-time.Minute) + `","message":{"role":"user","content":"Pick a colour"}}`)
	return write
}

func rolloutStamp(d time.Duration) string { return time.Now().UTC().Add(d).Format(time.RFC3339Nano) }

func TestNoOtherClaudeTranscriptRecordClosesAnOpenRequest(t *testing.T) {
	kinds := func(ts string) []string {
		return []string{
			`{"type":"assistant","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`,
			`{"type":"assistant","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}]}}`,
			`{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}}`,
			`{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":"an ordinary message"}}`,
			`{"type":"system","subtype":"api_error","timestamp":"` + ts + `"}`,
			`{"type":"system","subtype":"a_subtype_nobody_has_seen","timestamp":"` + ts + `"}`,
			`{"type":"summary","timestamp":"` + ts + `","summary":"x"}`,
			`{"type":"file-history-snapshot","timestamp":"` + ts + `"}`,
			`{"type":"a_record_kind_a_later_release_adds","timestamp":"` + ts + `"}`,
		}
	}
	for _, after := range []time.Duration{1 * time.Second, 3 * time.Second, 30 * time.Second} {
		r := newHoldRig(t, true)
		write := claudeTranscript(t, r)
		r.box("permission")
		require.True(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput)
		for _, line := range kinds(rolloutStamp(after)) {
			write(line)
			require.True(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput, "+%s: %s", after, line)
		}
		assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "claude", r.sid), "+%s: a person's message is still held", after)
	}
}

func TestClaudeTranscriptRecordsThatEndTheTurnCloseAnOpenRequest(t *testing.T) {
	for name, line := range map[string]func(ts string) string{
		"turn_duration (the turn ended)": func(ts string) string {
			return `{"type":"system","subtype":"turn_duration","timestamp":"` + ts + `","durationMs":900}`
		},
		"a refusal or interrupt of the tool": func(ts string) string {
			return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newHoldRig(t, true)
			write := claudeTranscript(t, r)
			r.box("permission")
			require.True(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput)
			write(line(rolloutStamp(3 * time.Second)))
			assert.False(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput)
			assert.False(t, r.h.blockedOnBox(context.Background(), r.name, "claude", r.sid))
		})
	}
}

// Claude runs tool calls in parallel: the PostToolUse of another call, of the same tool, does not answer a request that is waiting (the hook carries no call id; the server ties the request to the
// call's PreToolUse). Only the waiting call's own PostToolUse does.
func TestAParallelCallOfTheSameToolDoesNotAnswerAWaitingClaudeRequest(t *testing.T) {
	r := newHoldRig(t, true)
	r.h.pending.set(r.sid, pendingTool{ToolUseID: "call-a", ToolName: "Bash", At: time.Now()}) // the PreToolUse of the call that will wait
	r.box("permission")
	require.True(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput)
	evs, err := r.h.ts.ListAgentEvents(context.Background(), "chat", 20, &r.sid)
	require.NoError(t, err)
	tied := false
	for _, e := range evs {
		if e.EventType == "permission_request" && e.DetailJSON != nil {
			tied = strings.Contains(*e.DetailJSON, `"call_ids":["call-a"]`)
		}
	}
	require.True(t, tied, "the request is tied to its call")

	r.post(`{"event_type":"tool_use","summary":"Ran: date","tool_name":"Bash","tool_use_id":"call-b"}`) // an auto-allowed Bash finished beside it
	assert.True(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput, "another Bash call's PostToolUse does not answer it")
	assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "claude", r.sid), "so a person's message is still held")

	r.post(`{"event_type":"tool_use","summary":"Ran: rm x","tool_name":"Bash","tool_use_id":"call-a"}`)
	assert.False(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput, "its own call's PostToolUse does")
}

// The agent cannot choose which call its request is for: an id it puts in the detail is dropped.
func TestAnAgentCannotNameTheCallItsRequestIsFor(t *testing.T) {
	r := newHoldRig(t, true)
	r.h.pending.set(r.sid, pendingTool{ToolUseID: "call-a", ToolName: "Bash", At: time.Now()})
	r.post(`{"event_type":"permission_request","summary":"Waiting for the user: permission (Bash)","tool_name":"Bash","detail_json":{"kind":"permission","tool_name":"Bash","tool_use_id":"call-zzz","call_ids":["call-yyy"]}}`)
	evs, err := r.h.ts.ListAgentEvents(context.Background(), "chat", 20, &r.sid)
	require.NoError(t, err)
	for _, e := range evs {
		if e.EventType == "permission_request" && e.DetailJSON != nil {
			assert.NotContains(t, *e.DetailJSON, "call-zzz")
			assert.NotContains(t, *e.DetailJSON, "call-yyy")
			assert.Contains(t, *e.DetailJSON, "call-a", "the server's own record names it")
		}
	}
	// And another tool's pending call, or none, ties nothing.
	r2 := newHoldRig(t, true)
	r2.h.pending.set(r2.sid, pendingTool{ToolUseID: "call-r", ToolName: "Read", At: time.Now()})
	r2.post(`{"event_type":"permission_request","summary":"Waiting for the user: permission (Bash)","tool_name":"Bash","detail_json":{"kind":"permission","tool_name":"Bash","tool_use_id":"call-zzz","call_ids":["call-yyy"]}}`)
	evs, _ = r2.h.ts.ListAgentEvents(context.Background(), "chat", 20, &r2.sid)
	for _, e := range evs {
		if e.EventType == "permission_request" && e.DetailJSON != nil {
			assert.NotContains(t, *e.DetailJSON, "call_ids", "the pending call is another tool's: nothing is tied")
			assert.NotContains(t, *e.DetailJSON, "call-yyy")
		}
	}
}

// Security's hardest case: two PARALLEL calls of the same tool (two PreToolUse within microseconds, then the PermissionRequest with no call id): the server cannot tell which one the request is for, so it
// is tied to both. Neither call's PostToolUse closes it alone; the second does, and a person's message stays held until then.
func TestTwoParallelCallsOfTheSameToolHoldARequestUntilBothFinish(t *testing.T) {
	for _, first := range [][2]string{{"call-1", "call-2"}, {"call-2", "call-1"}} {
		r := newHoldRig(t, true)
		now := time.Now()
		r.h.pending.set(r.sid, pendingTool{ToolUseID: "call-1", ToolName: "Bash", At: now})
		r.h.pending.set(r.sid, pendingTool{ToolUseID: "call-2", ToolName: "Bash", At: now.Add(50 * time.Microsecond)})
		r.box("permission")
		evs, err := r.h.ts.ListAgentEvents(context.Background(), "chat", 20, &r.sid)
		require.NoError(t, err)
		for _, e := range evs {
			if e.EventType == "permission_request" && e.DetailJSON != nil {
				assert.Contains(t, *e.DetailJSON, `"call_ids":["call-1","call-2"]`, "tied to both: it cannot be told which waits")
			}
		}
		r.post(`{"event_type":"tool_use","summary":"Ran","tool_name":"Bash","tool_use_id":"` + first[0] + `"}`)
		assert.True(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput, "%s finishing first leaves it open", first[0])
		assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "claude", r.sid), "and the message stays held")
		r.post(`{"event_type":"tool_use","summary":"Ran","tool_name":"Bash","tool_use_id":"` + first[1] + `"}`)
		assert.False(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput, "the second closes it")
	}
}

// A call that finished before the request is not waited for, and a call of another tool is not either.
func TestOnlyCallsStillInFlightAreWaitedFor(t *testing.T) {
	r := newHoldRig(t, true)
	r.h.pending.set(r.sid, pendingTool{ToolUseID: "call-old", ToolName: "Bash", At: time.Now()})
	r.post(`{"event_type":"tool_use","summary":"Ran","tool_name":"Bash","tool_use_id":"call-old"}`)                     // finished
	r.h.pending.set(r.sid, pendingTool{ToolUseID: "call-long", ToolName: "Bash", At: time.Now().Add(-3 * time.Minute)}) // a long build still running: older than the window, so not what a new request is about
	r.h.pending.set(r.sid, pendingTool{ToolUseID: "call-read", ToolName: "Read", At: time.Now()})
	r.h.pending.set(r.sid, pendingTool{ToolUseID: "call-new", ToolName: "Bash", At: time.Now()})
	r.box("permission")
	evs, err := r.h.ts.ListAgentEvents(context.Background(), "chat", 20, &r.sid)
	require.NoError(t, err)
	for _, e := range evs {
		if e.EventType == "permission_request" && e.DetailJSON != nil {
			assert.Contains(t, *e.DetailJSON, `"call_ids":["call-new"]`)
		}
	}
	r.post(`{"event_type":"tool_use","summary":"Ran","tool_name":"Bash","tool_use_id":"call-new"}`)
	assert.False(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).NeedsInput)
}

// Codex has the same shape (Codex Test's #6657: two parallel execs, two PreToolUse, then the PermissionRequest with no call id), and a Codex request is held for both calls the same way.
func TestTwoParallelCodexCallsHoldARequestUntilBothFinish(t *testing.T) {
	r := newCodexRig(t)
	now := time.Now()
	r.line("event_msg", "task_started", -3*time.Second)
	r.post(`{"event_type":"prompt_submit","summary":"User submitted prompt"}`)
	r.h.pending.set(r.sid, pendingTool{ToolUseID: "exec-1", ToolName: "Bash", At: now})
	r.h.pending.set(r.sid, pendingTool{ToolUseID: "exec-2", ToolName: "Bash", At: now.Add(time.Millisecond)})
	r.post(`{"event_type":"permission_request","summary":"Waiting for the user: permission (Bash)","tool_name":"Bash","detail_json":{"kind":"permission","tool_name":"Bash"}}`)
	require.True(t, r.needsInput())
	r.post(`{"event_type":"tool_use","summary":"Ran","tool_name":"Bash","tool_use_id":"exec-2"}`) // the auto-approved call finishes first
	assert.True(t, r.needsInput(), "one of the two finishing leaves it open")
	assert.True(t, r.h.blockedOnBox(context.Background(), r.name, "codex", r.sid))
	r.post(`{"event_type":"tool_use","summary":"Ran","tool_name":"Bash","tool_use_id":"exec-1"}`)
	assert.False(t, r.needsInput(), "the approved call finishing closes it")
}
