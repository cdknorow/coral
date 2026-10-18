package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hook-payload corpus (#3481, #3475 phase 0): real payloads recorded from Claude Code
// 2.1.300 (personal paths and ids replaced), one per event and scenario. Each must map to
// the event Coral records, so a CLI update that changes a payload fails here first.
func TestClaudeHookCorpus(t *testing.T) {
	want := map[string]struct{ eventType, kind string }{
		"SessionStart":                      {"session_reset", ""},
		"UserPromptSubmit":                  {"prompt_submit", ""},
		"PermissionRequest-Bash":            {"permission_request", "permission"},
		"PermissionRequest-AskUserQuestion": {"permission_request", "question"},
		"PermissionRequest-ExitPlanMode":    {"permission_request", "plan"},
		"Notification-permission_prompt":    {"notification", "permission_prompt"},
		"PostToolUse-AskUserQuestion":       {"tool_use", ""},
		"PostToolUse-Write":                 {"tool_use", ""},
		"PostToolUse-ToolSearch":            {"tool_use", ""},
		"Stop":                              {"stop", ""},
		"Stop-active":                       {"stop", ""},
		"PostToolBatch":                     {"", ""}, // not an event Coral records
	}
	files, err := filepath.Glob("testdata/claude-2.1.300/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus: %v", err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if strings.HasPrefix(name, "PreToolUse-") {
			continue // goes to /pending-tool, not an event (main)
		}
		exp, ok := want[name]
		if !ok {
			t.Errorf("%s: corpus entry without an expectation", name)
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		event := parseAgenticEvent(d, d["hook_event_name"].(string), "sid")
		got, kind := "", ""
		if event != nil {
			got, _ = event["event_type"].(string)
			if detail, _ := event["detail_json"].(map[string]any); detail != nil {
				kind, _ = detail["kind"].(string)
				if k, _ := detail["notification_type"].(string); k != "" {
					kind = k
				}
			}
		}
		if got != exp.eventType || kind != exp.kind {
			t.Errorf("%s: got %q/%q, want %q/%q", name, got, kind, exp.eventType, exp.kind)
		}
	}
}

// Hooks documented for Claude Code but not reached in the phase-0 runs (no MCP server,
// no API error, no exit): their documented fields.
func TestRequestEventsFromDocumentedPayloads(t *testing.T) {
	cases := []struct {
		hook      string
		d         map[string]any
		eventType string
		kind      string
	}{
		{"Elicitation", map[string]any{"mcp_server_name": "db", "message": "Pick", "elicitation_id": "e1"}, "permission_request", "elicitation"},
		{"ElicitationResult", map[string]any{"mcp_server_name": "db", "action": "decline", "elicitation_id": "e1"}, "request_resolved", "elicitation"},
		{"StopFailure", map[string]any{"error": "overloaded", "last_assistant_message": "x"}, "stop", ""},
		{"SessionEnd", map[string]any{"reason": "prompt_input_exit"}, "session_end", ""},
	}
	for _, c := range cases {
		c.d["hook_event_name"] = c.hook
		event := parseAgenticEvent(c.d, c.hook, "sid")
		if event == nil {
			t.Fatalf("%s: no event", c.hook)
		}
		detail, _ := event["detail_json"].(map[string]any)
		kind, _ := detail["kind"].(string)
		if event["event_type"] != c.eventType || kind != c.kind {
			t.Errorf("%s: got %v/%q, want %s/%s", c.hook, event["event_type"], kind, c.eventType, c.kind)
		}
	}
}

// The agy corpus (#3481): real agy 1.3.5 hook payloads (paths and ids replaced). Its
// hooks are run as "coral-hook-agentic-state --agy <Event>" (the payload doesn't name the
// event); each must map to Coral's event, or be dropped.
func TestAgyHookCorpus(t *testing.T) {
	want := map[string]struct {
		hook, eventType, pendingTool string
	}{
		"SessionStart":    {"SessionStart", "session_reset", ""},
		"PreInvocation-0": {"PreInvocation", "prompt_submit", ""},
		"PreInvocation-1": {"PreInvocation", "", ""}, // a later model call of the same turn: dropped
		"PreToolUse":      {"PreToolUse", "", "run_command"},
		"PostToolUse":     {"PostToolUse", "tool_use", ""},
		"PostInvocation":  {"PostInvocation", "", ""},
		"Stop":            {"Stop", "stop", ""},
	}
	files, _ := filepath.Glob("testdata/agy-1.3.5/*.json")
	if len(files) != len(want) {
		t.Fatalf("%d corpus files, want %d", len(files), len(want))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		exp := want[name]
		raw, _ := os.ReadFile(f)
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		c, ok := fromAgy(d, exp.hook)
		if exp.eventType == "" && exp.pendingTool == "" {
			if ok {
				t.Errorf("%s: should be dropped, got %v", name, c)
			}
			continue
		}
		if !ok {
			t.Fatalf("%s: dropped", name)
		}
		if exp.pendingTool != "" {
			p := parsePendingTool(c, "sid")
			if p == nil || p["tool_name"] != exp.pendingTool || p["tool_use_id"] == "" {
				t.Errorf("%s: pending tool %v", name, p)
			}
			continue
		}
		event := parseAgenticEvent(c, c["hook_event_name"].(string), "sid")
		if event == nil || event["event_type"] != exp.eventType {
			t.Errorf("%s: got %v, want %s", name, event, exp.eventType)
			continue
		}
		if name == "PostToolUse" && (event["tool_name"] != "run_command" || strings.HasPrefix(event["summary"].(string), "Failed")) {
			t.Errorf("PostToolUse: %v (an empty error is not a failure)", event)
		}
		if name == "SessionStart" && event["summary"] != "Session started" {
			t.Errorf("SessionStart: %v (agy sends no source: a launch is a start, not a /clear, #4577)", event["summary"])
		}
		if name == "PostToolUse" {
			if detail, _ := event["detail_json"].(map[string]any); event["summary"] != "Ran: date" || detail["command"] != "date" {
				t.Errorf("PostToolUse: %v %v (agy's run_command reads as a command, #4577)", event["summary"], event["detail_json"])
			}
		}
		if name == "Stop" && event["summary"] != "Agent stopped: no_tool_call" {
			t.Errorf("Stop: %v", event["summary"])
		}
	}
	// A source agy names is kept.
	if c, _ := fromAgy(map[string]any{"source": "resume"}, "SessionStart"); parseAgenticEvent(c, "SessionStart", "sid")["summary"] != "Session resumed" {
		t.Error("a named source is kept")
	}
	// A failed tool call (agy reports error text) is a failure.
	c, _ := fromAgy(map[string]any{"toolCall": map[string]any{"name": "run_command"}, "error": "declined", "stepIdx": 2.0}, "PostToolUse")
	if e := parseAgenticEvent(c, "PostToolUse", "sid"); !strings.HasPrefix(e["summary"].(string), "Failed") {
		t.Errorf("failed tool: %v", e)
	}
}
