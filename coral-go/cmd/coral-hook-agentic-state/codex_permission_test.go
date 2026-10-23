package main

import (
	"strings"
	"testing"
)

// #6615: Codex's PermissionRequest hook (its input as the Codex 0.163.0 program describes it: session_id, turn_id, cwd, model, permission_mode, tool_name, tool_input, transcript_path;
// built from that description, not a capture of a real run) is a request to the person, as Claude's is: the agent's row goes to Needs input until the tool's own PostToolUse.
func TestCodexPermissionRequestIsARequestToThePerson(t *testing.T) {
	t.Setenv("CORAL_SESSION_NAME", "codex-11111111-2222-3333-4444-555555555555")
	e := parseAgenticEvent(map[string]any{
		"hook_event_name": "PermissionRequest", "session_id": "s", "turn_id": "t", "cwd": "/work", "model": "gpt-test", "permission_mode": "default",
		"tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf build"}, "transcript_path": "/x/rollout.jsonl",
	}, "PermissionRequest", "sid")
	if e == nil || e["event_type"] != "permission_request" {
		t.Fatalf("a request to the person: %v", e)
	}
	if !strings.HasPrefix(e["summary"].(string), "Waiting for the user: permission (Bash)") {
		t.Errorf("the summary says what it waits on: %v", e["summary"])
	}
	d, _ := e["detail_json"].(map[string]any)
	if d["kind"] != "permission" || d["tool_name"] != "Bash" {
		t.Errorf("its kind and tool: %v", d)
	}
	if strings.Contains(strings.ToLower(e["summary"].(string)), "rm -rf") {
		t.Errorf("the command is not copied into the event's summary: %v", e["summary"])
	}
	if _, has := d["command"]; has {
		t.Errorf("nor its detail: %v", d)
	}
}
