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

func TestParseAgenticEventWaitingNotificationRemainsNotification(t *testing.T) {
	event := parseAgenticEvent(map[string]any{
		"hook_event_name": "Notification",
		"message":         "Claude is waiting for your input",
	}, "Notification", "session-1")
	if event == nil {
		t.Fatal("expected event")
	}
	if event["event_type"] != "notification" {
		t.Fatalf("expected notification, got %v", event["event_type"])
	}
}

func TestParseAgenticEventKeepsToolTimingAndResponseMetadata(t *testing.T) {
	event := parseAgenticEvent(map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "WebFetch",
		"tool_use_id":     "toolu_123",
		"duration_ms":     float64(2936),
		"tool_input":      map[string]any{"url": "https://example.com/docs"},
		"tool_response": map[string]any{
			"code": float64(200), "codeText": "OK", "bytes": float64(207450), "durationMs": float64(2901),
			"result": "a very long page body that must not be stored",
		},
	}, "PostToolUse", "session-1")

	if event == nil {
		t.Fatal("expected event")
	}
	if event["tool_use_id"] != "toolu_123" {
		t.Fatalf("tool_use_id = %v", event["tool_use_id"])
	}
	if event["duration_ms"] != int64(2936) {
		t.Fatalf("duration_ms = %v", event["duration_ms"])
	}
	detail, _ := event["detail_json"].(map[string]any)
	if detail["url"] != "https://example.com/docs" {
		t.Fatalf("url = %v", detail["url"])
	}
	resp, _ := detail["response"].(map[string]any)
	if resp["code"] != float64(200) || resp["codeText"] != "OK" || resp["bytes"] != float64(207450) {
		t.Fatalf("unexpected response detail: %v", resp)
	}
	if _, kept := resp["result"]; kept {
		t.Fatal("bulk response content must not be stored")
	}
}

func TestParseAgenticEventToolFailure(t *testing.T) {
	event := parseAgenticEvent(map[string]any{
		"hook_event_name": "PostToolUseFailure",
		"tool_name":       "mcp__github__create_issue",
		"tool_use_id":     "toolu_456",
		"duration_ms":     float64(812),
		"tool_input":      map[string]any{"title": "x"},
		"error":           "request timed out",
		"is_interrupt":    false,
	}, "PostToolUseFailure", "session-1")

	if event == nil {
		t.Fatal("expected event")
	}
	if event["event_type"] != "tool_use" || event["summary"] != "Failed: Used mcp__github__create_issue" {
		t.Fatalf("unexpected event: %v", event)
	}
	if event["duration_ms"] != int64(812) {
		t.Fatalf("duration_ms = %v", event["duration_ms"])
	}
	detail, _ := event["detail_json"].(map[string]any)
	if detail["failed"] != true || detail["error"] != "request timed out" {
		t.Fatalf("unexpected detail: %v", detail)
	}
	if _, set := detail["interrupted"]; set {
		t.Fatal("interrupted must only be set for an interrupt")
	}
}

func TestParseAgenticEventWithoutTimingStaysUnchanged(t *testing.T) {
	event := parseAgenticEvent(map[string]any{
		"tool_name":  "Read",
		"tool_input": map[string]any{"file_path": "/repo/main.go"},
	}, "PostToolUse", "session-1")
	if _, set := event["duration_ms"]; set {
		t.Fatal("duration_ms must be absent when the agent does not report it")
	}
	if _, set := event["tool_use_id"]; set {
		t.Fatal("tool_use_id must be absent when the agent does not report it")
	}
}

func TestParsePendingTool(t *testing.T) {
	got := parsePendingTool(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "AskUserQuestion",
		"tool_use_id":     "toolu_1",
		"tool_input":      map[string]any{"questions": []any{map[string]any{"question": "Which?"}}},
	}, "session-1")
	if got == nil || got["tool_name"] != "AskUserQuestion" || got["tool_use_id"] != "toolu_1" || got["session_id"] != "session-1" {
		t.Fatalf("pending = %#v", got)
	}
	if in, _ := got["tool_input"].(map[string]any); in == nil || in["questions"] == nil {
		t.Fatalf("tool_input not forwarded: %#v", got)
	}
	if parsePendingTool(map[string]any{"hook_event_name": "PreToolUse"}, "s") != nil {
		t.Fatal("a PreToolUse without a tool name must be dropped")
	}
}
