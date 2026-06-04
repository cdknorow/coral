package jsonl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadNewMessages_Claude(t *testing.T) {
	// Create a temp JSONL file
	dir := t.TempDir()
	sessionID := "test-session-123"

	// Set CLAUDE_PROJECTS_DIR so the reader can find the file
	projectDir := filepath.Join(dir, "test-project")
	os.MkdirAll(projectDir, 0755)
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	jsonlPath := filepath.Join(projectDir, sessionID+".jsonl")

	// Write some JSONL entries
	entries := `{"type":"user","timestamp":"2024-01-01T00:00:00Z","message":{"content":"Hello world"}}
{"type":"assistant","timestamp":"2024-01-01T00:00:01Z","message":{"content":[{"type":"text","text":"Hi there!"}]}}
{"type":"assistant","timestamp":"2024-01-01T00:00:02Z","message":{"content":[{"type":"text","text":"Let me help."},{"type":"tool_use","id":"tu_1","name":"Read","input":{"file_path":"/tmp/test.go"}}]}}
`
	os.WriteFile(jsonlPath, []byte(entries), 0644)

	reader := NewSessionReader()

	// First read
	msgs, total := reader.ReadNewMessages(sessionID, "", "claude")
	if total != 3 {
		t.Fatalf("expected 3 total messages, got %d", total)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 new messages, got %d", len(msgs))
	}

	// Check user message
	if msgs[0]["type"] != "user" {
		t.Errorf("expected user type, got %s", msgs[0]["type"])
	}
	if msgs[0]["content"] != "Hello world" {
		t.Errorf("expected 'Hello world', got %s", msgs[0]["content"])
	}

	// Check assistant message
	if msgs[1]["type"] != "assistant" {
		t.Errorf("expected assistant type, got %s", msgs[1]["type"])
	}
	if msgs[1]["text"] != "Hi there!" {
		t.Errorf("expected 'Hi there!', got %s", msgs[1]["text"])
	}

	// Check assistant with tool use
	if msgs[2]["type"] != "assistant" {
		t.Errorf("expected assistant type, got %s", msgs[2]["type"])
	}
	toolUses, ok := msgs[2]["tool_uses"].([]map[string]any)
	if !ok || len(toolUses) != 1 {
		t.Fatalf("expected 1 tool use, got %v", msgs[2]["tool_uses"])
	}
	if toolUses[0]["name"] != "Read" {
		t.Errorf("expected tool name 'Read', got %s", toolUses[0]["name"])
	}
	if toolUses[0]["input_summary"] != "/tmp/test.go" {
		t.Errorf("expected input_summary '/tmp/test.go', got %s", toolUses[0]["input_summary"])
	}

	// Second read — no new data
	msgs2, total2 := reader.ReadNewMessages(sessionID, "", "claude")
	if len(msgs2) != 0 {
		t.Errorf("expected 0 new messages, got %d", len(msgs2))
	}
	if total2 != 3 {
		t.Errorf("expected 3 total, got %d", total2)
	}

	// Append more data
	f, _ := os.OpenFile(jsonlPath, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString(`{"type":"user","timestamp":"2024-01-01T00:01:00Z","message":{"content":"Thanks!"}}` + "\n")
	f.Close()

	msgs3, total3 := reader.ReadNewMessages(sessionID, "", "claude")
	if len(msgs3) != 1 {
		t.Errorf("expected 1 new message, got %d", len(msgs3))
	}
	if total3 != 4 {
		t.Errorf("expected 4 total, got %d", total3)
	}
}

func TestReadNewMessages_ToolResult(t *testing.T) {
	dir := t.TempDir()
	sessionID := "test-tool-result"
	projectDir := filepath.Join(dir, "test-project")
	os.MkdirAll(projectDir, 0755)
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	jsonlPath := filepath.Join(projectDir, sessionID+".jsonl")

	entries := `{"type":"assistant","timestamp":"T1","message":{"content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls -la","description":"list files"}}]}}
{"type":"user","timestamp":"T2","message":{"content":[{"type":"tool_result","tool_use_id":"tu_1","content":"total 42\ndrwxr-xr-x  5 user staff"}]}}
`
	os.WriteFile(jsonlPath, []byte(entries), 0644)

	reader := NewSessionReader()
	msgs, total := reader.ReadNewMessages(sessionID, "", "claude")

	if total != 2 {
		t.Fatalf("expected 2 messages, got %d", total)
	}

	// First: assistant with tool use
	toolUses := msgs[0]["tool_uses"].([]map[string]any)
	if toolUses[0]["name"] != "Bash" {
		t.Errorf("expected Bash, got %s", toolUses[0]["name"])
	}
	if toolUses[0]["command"] != "ls -la" {
		t.Errorf("expected command 'ls -la', got %v", toolUses[0]["command"])
	}

	// Second: tool result with resolved name
	if msgs[1]["type"] != "tool_result" {
		t.Errorf("expected tool_result, got %s", msgs[1]["type"])
	}
	if msgs[1]["tool_name"] != "Bash" {
		t.Errorf("expected tool_name 'Bash', got %s", msgs[1]["tool_name"])
	}
}

func TestReadNewMessages_CodexEventMessages(t *testing.T) {
	dir := t.TempDir()
	sessionID := "019e90eb-a08a-7511-a410-23e7ae3e62a8"
	codexHome := filepath.Join(dir, ".codex")
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "06", "03")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	jsonlPath := filepath.Join(sessionDir, "rollout-2026-06-03T21-37-01-"+sessionID+".jsonl")
	entries := `{"timestamp":"2026-06-04T04:37:22.293Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"hidden developer instructions"}]}}
{"timestamp":"2026-06-04T04:37:22.296Z","type":"event_msg","payload":{"type":"user_message","message":"fix codex chat history","images":[]}}
{"timestamp":"2026-06-04T04:37:28.017Z","type":"event_msg","payload":{"type":"agent_message","message":"||PULSE:STATUS Working||\nI found the issue.","phase":"commentary"}}
`
	if err := os.WriteFile(jsonlPath, []byte(entries), 0644); err != nil {
		t.Fatal(err)
	}

	reader := NewSessionReader()
	msgs, total := reader.ReadNewMessages(sessionID, "", "codex")

	if total != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", total, msgs)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 new messages, got %d", len(msgs))
	}
	if msgs[0]["type"] != "user" || msgs[0]["content"] != "fix codex chat history" {
		t.Fatalf("unexpected user message: %#v", msgs[0])
	}
	if msgs[1]["type"] != "assistant" || msgs[1]["text"] != "I found the issue." {
		t.Fatalf("unexpected assistant message: %#v", msgs[1])
	}
}

func TestReadNewMessages_CodexSessionMarker(t *testing.T) {
	dir := t.TempDir()
	coralSessionID := "9fccfe64-ac70-8ed7-af83-a76fa139c0a9"
	codexSessionID := "019e90eb-a08a-7511-a410-23e7ae3e62a8"
	codexHome := filepath.Join(dir, ".codex")
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "06", "03")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	jsonlPath := filepath.Join(sessionDir, "rollout-2026-06-03T21-37-01-"+codexSessionID+".jsonl")
	entries := `{"timestamp":"2026-06-04T04:37:22.293Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\nCORAL_SESSION_ID: 9fccfe64-ac70-8ed7-af83-a76fa139c0a9"}]}}
{"timestamp":"2026-06-04T04:37:22.296Z","type":"event_msg","payload":{"type":"user_message","message":"hello from coral session","images":[]}}
`
	if err := os.WriteFile(jsonlPath, []byte(entries), 0644); err != nil {
		t.Fatal(err)
	}

	reader := NewSessionReader()
	msgs, total := reader.ReadNewMessages(coralSessionID, "", "codex")

	if total != 1 {
		t.Fatalf("expected 1 message, got %d: %#v", total, msgs)
	}
	if len(msgs) != 1 || msgs[0]["content"] != "hello from coral session" {
		t.Fatalf("unexpected messages: %#v", msgs)
	}
}

func TestClearSession(t *testing.T) {
	reader := NewSessionReader()
	reader.cache["test"] = &sessionCache{path: "/tmp/test.jsonl"}
	reader.ClearSession("test")
	if _, ok := reader.cache["test"]; ok {
		t.Error("expected session to be cleared")
	}
}

func TestSummarizeToolInput(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]any
		want     string
	}{
		{"Read", "Read", map[string]any{"file_path": "/foo/bar.go"}, "/foo/bar.go"},
		{"Bash", "Bash", map[string]any{"command": "echo hello"}, "echo hello"},
		{"Grep", "Grep", map[string]any{"pattern": "TODO", "path": "src/"}, "TODO in src/"},
		{"WebSearch", "WebSearch", map[string]any{"query": "golang pty"}, "golang pty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeToolInput(tt.toolName, tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPulseStripping(t *testing.T) {
	entry := map[string]any{
		"type":      "assistant",
		"timestamp": "T1",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "Working on it ||PULSE:STATUS doing stuff|| now"},
			},
		},
	}
	toolNames := make(map[string]string)
	msgs := parseClaudeEntry(entry, toolNames)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0]["text"] != "Working on it  now" {
		t.Errorf("expected PULSE stripped, got %q", msgs[0]["text"])
	}
}
