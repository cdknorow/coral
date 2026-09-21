package jsonl

import (
	"os"
	"path/filepath"
	"strings"
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

func TestReadAllMessages_CodexRolloutToolCalls(t *testing.T) {
	dir := t.TempDir()
	sessionID := "019e9db7-58df-7082-a954-7305c01b1489"
	codexHome := filepath.Join(dir, ".codex")
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "09", "20")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	fixture, err := os.ReadFile(filepath.Join("testdata", "codex_rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "rollout-2026-09-20T10-00-00-"+sessionID+".jsonl"), fixture, 0644); err != nil {
		t.Fatal(err)
	}

	msgs, total := NewSessionReader().ReadAllMessages(sessionID, "", "codex")

	type row struct{ kind, text, tool, id string }
	var got []row
	for _, m := range msgs {
		r := row{kind: m["type"].(string)}
		switch r.kind {
		case "user":
			r.text = m["content"].(string)
		case "assistant":
			r.text = m["text"].(string)
			if tools, _ := m["tool_uses"].([]map[string]any); len(tools) == 1 {
				r.tool, r.id = tools[0]["name"].(string), tools[0]["tool_use_id"].(string)
			}
		case "tool_result":
			r.tool, r.id = m["tool_name"].(string), m["tool_use_id"].(string)
		}
		got = append(got, r)
	}
	want := []row{
		{kind: "user", text: "Build a snowy mountain page."},
		{kind: "assistant", text: "I'll inspect the project structure first."},
		{kind: "assistant", tool: "exec_command", id: "call_exec1"},
		{kind: "tool_result", tool: "exec_command", id: "call_exec1"},
		{kind: "assistant", tool: "shell", id: "call_shell1"},
		{kind: "tool_result", tool: "shell", id: "call_shell1"},
		{kind: "assistant", text: "The folder is empty, so I'll add the page files."},
		{kind: "assistant", tool: "apply_patch", id: "call_patch1"},
		{kind: "tool_result", tool: "apply_patch", id: "call_patch1"},
		{kind: "assistant", tool: "view_image", id: "call_img1"},
		{kind: "tool_result", tool: "view_image", id: "call_img1"},
		{kind: "assistant", text: "Added index.html and styles.css. Open index.html to explore the mountains."},
		{kind: "user", text: "Thanks! What did you name the title?"},
		{kind: "assistant", text: "The page title is Snowy Mountains."},
	}
	if total != len(want) || len(got) != len(want) {
		t.Fatalf("expected %d messages (no duplicated response_item messages), got total=%d:\n%+v", len(want), total, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d: got %+v, want %+v", i, got[i], want[i])
		}
	}

	tool := func(i int) map[string]any { return msgs[i]["tool_uses"].([]map[string]any)[0] }
	if tool(2)["command"] != "pwd && rg --files | head -200" {
		t.Errorf("exec_command cmd not surfaced as command: %#v", tool(2))
	}
	if tool(4)["command"] != "bash -lc ls -la" {
		t.Errorf("shell argv not joined into command: %#v", tool(4))
	}
	if msgs[5]["content"] != "total 8\nREADME.md\n" {
		t.Errorf("JSON-wrapped shell output not unwrapped: %q", msgs[5]["content"])
	}
	if tool(7)["input_summary"] != "index.html, styles.css" {
		t.Errorf("apply_patch summary should list files: %#v", tool(7)["input_summary"])
	}
	if patch, _ := tool(7)["patch"].(string); !strings.Contains(patch, "+body { margin: 0; }") {
		t.Errorf("apply_patch patch body missing: %q", patch)
	}
	if tool(9)["input_summary"] != "/repo/game/shot.png" {
		t.Errorf("view_image summary should be its path: %#v", tool(9)["input_summary"])
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
	reader.firstPrompts["test"] = &firstPromptCache{path: "/tmp/test.jsonl", prompt: "hello"}
	reader.ClearSession("test")
	if _, ok := reader.cache["test"]; ok {
		t.Error("expected session to be cleared")
	}
	if _, ok := reader.firstPrompts["test"]; ok {
		t.Error("expected first-prompt cache to be cleared")
	}
}

func TestFirstUserPrompt(t *testing.T) {
	dir := t.TempDir()
	sessionID := "first-prompt-session"
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	entries := `{"type":"assistant","message":{"content":"Before the user"}}
{"type":"user","message":{"content":"<system-reminder>ignore me</system-reminder>"}}
{"type":"user","message":{"content":"  Build the dashboard  "}}
{"type":"user","message":{"content":"Second prompt"}}
`
	if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(entries), 0644); err != nil {
		t.Fatal(err)
	}

	reader := NewSessionReader()
	if got := reader.FirstUserPrompt(sessionID, "", "claude"); got != "Build the dashboard" {
		t.Fatalf("FirstUserPrompt() = %q, want %q", got, "Build the dashboard")
	}
	if _, ok := reader.cache[sessionID]; ok {
		t.Fatal("FirstUserPrompt populated the full transcript message cache")
	}
	if got := reader.firstPrompts[sessionID]; got == nil || got.prompt != "Build the dashboard" {
		t.Fatalf("first-prompt cache = %#v", got)
	} else if info, err := os.Stat(filepath.Join(projectDir, sessionID+".jsonl")); err != nil {
		t.Fatal(err)
	} else if got.offset >= info.Size() {
		t.Fatalf("first-prompt scan consumed full transcript: offset=%d size=%d", got.offset, info.Size())
	}
	if got := reader.FirstUserPrompt("missing-session", "", "claude"); got != "" {
		t.Fatalf("FirstUserPrompt() for missing history = %q, want empty string", got)
	}
}

func TestFirstUserPrompt_TerminalSkipsTranscriptLookup(t *testing.T) {
	reader := NewSessionReader()
	if got := reader.FirstUserPrompt("terminal-session", "/tmp", "terminal"); got != "" {
		t.Fatalf("terminal prompt = %q, want empty", got)
	}
	if _, ok := reader.firstPrompts["terminal-session"]; ok {
		t.Fatal("terminal session populated first-prompt path cache")
	}
}

func TestFirstUserPrompt_IncrementalAndClear(t *testing.T) {
	dir := t.TempDir()
	sessionID := "incremental-first-prompt"
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	path := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"assistant","message":{"content":"waiting"}}`+"\n"+`{"type":"user","message":{"content":"partial`), 0644); err != nil {
		t.Fatal(err)
	}

	reader := NewSessionReader()
	if got := reader.FirstUserPrompt(sessionID, "", "claude"); got != "" {
		t.Fatalf("prompt before record completed = %q, want empty", got)
	}
	if _, ok := reader.cache[sessionID]; ok {
		t.Fatal("incremental first-prompt read populated full transcript cache")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(` prompt"}}` + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := reader.FirstUserPrompt(sessionID, "", "claude"); got != "partial prompt" {
		t.Fatalf("prompt after append = %q, want %q", got, "partial prompt")
	}

	reader.ClearSession(sessionID)
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"content":"replacement prompt"}}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := reader.FirstUserPrompt(sessionID, "", "claude"); got != "replacement prompt" {
		t.Fatalf("prompt after ClearSession = %q, want %q", got, "replacement prompt")
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
