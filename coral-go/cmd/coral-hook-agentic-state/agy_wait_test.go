package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/sessionstate"
)

// #4819: agy sends no hook for a question to the user, only PreToolUse before every tool. Its ask_question always waits for the user's
// answer, so the hook records the request Claude's PermissionRequest records. The payloads have the keys of agy 1.4.2's real PostToolUse
// (toolCall, stepIdx, conversationId, modelName); the ask_question arguments are the shape the Antigravity CLI tester described, not a capture.
func agyPre(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "agy-1.4.2", name))
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	c, ok := fromAgy(d, "PreToolUse")
	if !ok {
		t.Fatalf("%s: dropped", name)
	}
	return c
}

func TestAgyAskQuestionIsRecordedAsAWaitAndAnswered(t *testing.T) {
	pre := agyPre(t, "PreToolUse-ask_question.json")
	ev := agyWaitEvent(pre, "PreToolUse", "sid")
	if ev == nil {
		t.Fatal("an ask_question PreToolUse records a request")
	}
	detail, _ := ev["detail_json"].(map[string]any)
	if ev["event_type"] != "permission_request" || ev["tool_name"] != "ask_question" || ev["tool_use_id"] != "agy-step-4" || detail["kind"] != "question" || ev["session_id"] != "sid" {
		t.Fatalf("the request: %v", ev)
	}
	// what the state derivation makes of it: waiting, kind question; the tool's own PostToolUse answers it
	at0 := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	events := []sessionstate.Event{
		{Type: "prompt_submit", At: at0},
		{Type: "permission_request", Kind: "question", ToolName: "ask_question", Summary: ev["summary"].(string), At: at0.Add(time.Second)},
	}
	st := sessionstate.Derive(sessionstate.Input{Events: events})
	if !st.NeedsInput || st.WaitingKind != "question" || !st.OwnRequest {
		t.Fatalf("open question: %+v", st)
	}
	post := agyPostEvent(t, "ask_question", 4)
	if post["event_type"] != "tool_use" || post["tool_name"] != "ask_question" {
		t.Fatalf("the answer arrives as the tool's PostToolUse: %v", post)
	}
	answered := append(events, sessionstate.Event{Type: "tool_use", ToolName: "ask_question", At: at0.Add(5 * time.Second)})
	if st = sessionstate.Derive(sessionstate.Input{Events: answered}); st.NeedsInput || !st.InTurn {
		t.Fatalf("answered: %+v", st)
	}
	// idle at the turn's end: awaiting the user, not waiting on a request
	idle := append(answered, sessionstate.Event{Type: "stop", At: at0.Add(9 * time.Second)})
	if st = sessionstate.Derive(sessionstate.Input{Events: idle}); st.NeedsInput || !st.AwaitingUser {
		t.Fatalf("idle: %+v", st)
	}
	// a question left open when the turn ends (the user skipped it) is closed by the stop
	skipped := append(events, sessionstate.Event{Type: "stop", At: at0.Add(9 * time.Second)})
	if st = sessionstate.Derive(sessionstate.Input{Events: skipped}); st.NeedsInput || !st.AwaitingUser {
		t.Fatalf("skipped: %+v", st)
	}
}

// agyPostEvent is the event the hook records for the tool's PostToolUse (the payload of a PostToolUse has a tool call with the same keys).
func agyPostEvent(t *testing.T, tool string, step int) map[string]any {
	t.Helper()
	d := agyPre(t, "PreToolUse-ask_question.json")
	d["hook_event_name"] = "PostToolUse"
	d["tool_name"] = tool
	d["tool_use_id"] = "agy-step-4"
	return parseAgenticEvent(d, "PostToolUse", "sid")
}

func TestAgyOtherToolsAndOtherAgentsRecordNoWait(t *testing.T) {
	if ev := agyWaitEvent(agyPre(t, "PreToolUse-run_command.json"), "PreToolUse", "sid"); ev != nil {
		t.Fatalf("a run_command may run without asking: %v", ev)
	}
	if ev := agyWaitEvent(agyPre(t, "PreToolUse-ask_question.json"), "", "sid"); ev != nil {
		t.Fatalf("not an agy hook: %v", ev)
	}
}
