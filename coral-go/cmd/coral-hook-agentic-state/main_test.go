package main

import "testing"

func TestParseAgenticEventCodexToolPayload(t *testing.T) {
	event := parseAgenticEvent(map[string]any{
		"event": "PostToolUse",
		"tool": map[string]any{
			"name": "Bash",
			"input": map[string]any{
				"command": "go test ./internal/agent",
			},
		},
	}, "PostToolUse", "codex-session-1")

	if event == nil {
		t.Fatal("expected event")
	}
	if event["event_type"] != "tool_use" {
		t.Fatalf("expected tool_use, got %v", event["event_type"])
	}
	if event["tool_name"] != "Bash" {
		t.Fatalf("expected Bash tool, got %v", event["tool_name"])
	}
	if event["summary"] != "Ran: go test ./internal/agent" {
		t.Fatalf("unexpected summary: %v", event["summary"])
	}
}

func TestParseAgenticEventCodexPromptPayload(t *testing.T) {
	event := parseAgenticEvent(map[string]any{
		"event_name": "UserPromptSubmit",
		"prompt":     "fix activity",
	}, "UserPromptSubmit", "codex-session-1")

	if event == nil {
		t.Fatal("expected event")
	}
	if event["event_type"] != "prompt_submit" {
		t.Fatalf("expected prompt_submit, got %v", event["event_type"])
	}
}
