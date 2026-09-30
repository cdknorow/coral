package jsonl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
)

func TestResolveCodexTranscriptByMarkerUsesTrustedDeveloperMetadata(t *testing.T) {
	base := filepath.Join(t.TempDir(), "sessions", "2026", "09", "29")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	coralID := "00000000-0000-0000-0000-000000000801"
	falseMatch := filepath.Join(base, "rollout-false.jsonl")
	if err := os.WriteFile(falseMatch, []byte(fmt.Sprintf(`{"type":"event_msg","payload":{"type":"user_message","message":"CORAL_SESSION_ID: %s"}}`+"\n", coralID)), 0644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(base, "rollout-old.jsonl")
	newer := filepath.Join(base, "rollout-new.jsonl")
	developer := func(extra string) string {
		return fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\\nCORAL_SESSION_ID: %s"}]}}`+"\n%s", coralID, extra)
	}
	if err := os.WriteFile(old, []byte(developer("")), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte(developer("")), 0644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, now, now); err != nil {
		t.Fatal(err)
	}
	if got := codexTranscriptCoralSessionID(falseMatch); got != "" {
		t.Fatalf("user text must not identify transcript, got %q", got)
	}
	if got := resolveCodexTranscriptByMarker(filepath.Join(t.TempDir(), "missing"), coralID); got != "" {
		t.Fatalf("missing directory unexpectedly resolved %q", got)
	}
	if got := resolveCodexTranscriptByMarker(filepath.Dir(base), coralID); got != newer {
		t.Fatalf("resolved %q, want newest trusted transcript %q", got, newer)
	}
}

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
	if msgs[1]["phase"] != "commentary" {
		t.Fatalf("Codex message phase was not preserved: %#v", msgs[1])
	}
}

func TestReadNewMessages_CodexImageAndTextMessages(t *testing.T) {
	dir := t.TempDir()
	sessionID := "019e90eb-a08a-7511-a410-23e7ae3e9999"
	codexHome := filepath.Join(dir, ".codex")
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "09", "29")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	jsonlPath := filepath.Join(sessionDir, "rollout-2026-09-29T12-00-00-"+sessionID+".jsonl")
	entries := `{"timestamp":"2026-09-29T12:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"","images":["/Users/test/.coral/uploads/image1.png"]}}
{"timestamp":"2026-09-29T12:00:02.000Z","type":"event_msg","payload":{"type":"user_message","message":"Here is text with image","images":["/Users/test/.coral/uploads/image2.png"]}}
{"timestamp":"2026-09-29T12:00:03.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_image","image_url":{"url":"/Users/test/.coral/uploads/image3.png"}}]}}
{"timestamp":"2026-09-29T12:00:04.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_image","image_url":{"url":"/Users/test/.coral/uploads/image4.png"}},{"type":"input_text","text":"Caption for image 4"}]}}
`
	if err := os.WriteFile(jsonlPath, []byte(entries), 0644); err != nil {
		t.Fatal(err)
	}

	reader := NewSessionReader()
	msgs, total := reader.ReadNewMessages(sessionID, "", "codex")

	if total != 4 {
		t.Fatalf("expected 4 messages, got %d: %#v", total, msgs)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 new messages, got %d", len(msgs))
	}
	// Image only in event_msg should extract image path
	if msgs[0]["type"] != "user" || msgs[0]["content"] != "/Users/test/.coral/uploads/image1.png" {
		t.Fatalf("unexpected msgs[0]: %#v", msgs[0])
	}
	// Image + text in event_msg should extract text
	if msgs[1]["type"] != "user" || msgs[1]["content"] != "Here is text with image" {
		t.Fatalf("unexpected msgs[1]: %#v", msgs[1])
	}
	// Image only in response_item should extract image URL/path
	if msgs[2]["type"] != "user" || msgs[2]["content"] != "/Users/test/.coral/uploads/image3.png" {
		t.Fatalf("unexpected msgs[2]: %#v", msgs[2])
	}
	// Image + text in response_item should preserve text
	if msgs[3]["type"] != "user" || msgs[3]["content"] != "/Users/test/.coral/uploads/image4.png\nCaption for image 4" {
		t.Fatalf("unexpected msgs[3]: %#v", msgs[3])
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
	if msgs[1]["phase"] != "commentary" || msgs[11]["phase"] != "final_answer" {
		t.Errorf("Codex phases not preserved: commentary=%v final=%v", msgs[1]["phase"], msgs[11]["phase"])
	}
}

func TestCodexFunctionCall_CompositeAndQuestionSummaries(t *testing.T) {
	composite := codexFunctionCall("functions.exec", "call-1", `{"input":"const r = await tools.exec_command({cmd:\"pwd\"}); text(r.output);"}`)
	if composite["input_summary"] != "exec_command" {
		t.Fatalf("composite summary = %v, want nested tool name", composite["input_summary"])
	}

	question := codexFunctionCall("functions.request_user_input", "call-2", `{"questions":[{"question":"Which?","options":[]}]}`)
	questions, ok := question["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("questions not surfaced: %#v", question)
	}

	wrapped := codexCustomToolCall("exec", "call-3", `const r = await tools.view_image({path:"shot.png"}); image(r.image_url);`)
	if wrapped["input_summary"] != "view_image" || wrapped["operation"] != "view_image" {
		t.Fatalf("wrapped tool not normalized: %#v", wrapped)
	}
}

func TestReadNewMessages_CurrentCodexResponseItems(t *testing.T) {
	dir := t.TempDir()
	sessionID := "019f-current-codex"
	codexHome := filepath.Join(dir, ".codex")
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "09", "21")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	entries := `{"timestamp":"2026-09-21T10:00:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>hidden</environment_context>"}]}}
{"timestamp":"2026-09-21T10:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Fix the chat."}]}}
{"timestamp":"2026-09-21T10:00:02.000Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I am inspecting it."}]}}
{"timestamp":"2026-09-21T10:00:03.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"call-1","input":"const r = await tools.exec_command({cmd:\"pwd\"}); text(r.output);"}}
{"timestamp":"2026-09-21T10:00:04.000Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call-1","output":[{"type":"input_text","text":"Script completed\n"},{"type":"input_text","text":"done"}]}}
{"timestamp":"2026-09-21T10:00:05.000Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Fixed."}]}}
`
	path := filepath.Join(sessionDir, "rollout-2026-09-21T10-00-00-"+sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(entries), 0644); err != nil {
		t.Fatal(err)
	}

	msgs, total := NewSessionReader().ReadNewMessages(sessionID, "", "codex")
	if total != 5 || len(msgs) != 5 {
		t.Fatalf("current rollout parsed %d messages: %#v", total, msgs)
	}
	if msgs[0]["type"] != "user" || msgs[0]["content"] != "Fix the chat." {
		t.Fatalf("injected context was not filtered: %#v", msgs[0])
	}
	if msgs[1]["phase"] != "commentary" || msgs[4]["phase"] != "final_answer" {
		t.Fatalf("response_item phases missing: %#v", msgs)
	}
	tool := msgs[2]["tool_uses"].([]map[string]any)[0]
	if tool["name"] != "exec" || tool["operation"] != "exec_command" || tool["input_summary"] != "exec_command" {
		t.Fatalf("wrapped tool not converted: %#v", tool)
	}
	if msgs[3]["content"] != "Script completed\ndone" {
		t.Fatalf("array tool output not converted: %#v", msgs[3])
	}
	if msgs[3]["tool_name"] != "exec_command" {
		t.Fatalf("wrapped result did not retain its operation: %#v", msgs[3])
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

func TestReadNewMessages_SkipsMetaUserText(t *testing.T) {
	dir := t.TempDir()
	sessionID := "meta-session"
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	// A loaded skill arrives as a user entry flagged isMeta, in either content form.
	entries := `{"type":"user","isMeta":true,"message":{"content":[{"type":"text","text":"# Dev Screenshot: Build, Reload, and Capture UI"}]}}
{"type":"user","isMeta":true,"message":{"content":"Base directory for this skill: /x"}}
{"type":"user","message":{"content":"Take a screenshot"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","isMeta":true,"message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"a.txt"}]}}
`
	if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(entries), 0644); err != nil {
		t.Fatal(err)
	}

	reader := NewSessionReader()
	msgs, _ := reader.ReadNewMessages(sessionID, "", "claude")
	var users []string
	var results int
	for _, m := range msgs {
		switch m["type"] {
		case "user":
			users = append(users, m["content"].(string))
		case "tool_result":
			results++
		}
	}
	if len(users) != 1 || users[0] != "Take a screenshot" {
		t.Fatalf("user messages = %q, want only the typed prompt", users)
	}
	if results != 1 {
		t.Fatalf("tool results = %d, want 1 (meta entries still carry tool results)", results)
	}
	if got := reader.FirstUserPrompt(sessionID, "", "claude"); got != "Take a screenshot" {
		t.Fatalf("FirstUserPrompt() = %q, want the typed prompt, not skill text", got)
	}
}

func TestParseClaudeEntry_QueuedCommand(t *testing.T) {
	// A message sent mid-turn is logged as a queued_command attachment.
	human := map[string]any{
		"type":      "attachment",
		"timestamp": "2026-09-22T04:46:34.764Z",
		"attachment": map[string]any{
			"type":        "queued_command",
			"prompt":      "hmm, maybe thats not true",
			"commandMode": "prompt",
			"origin":      map[string]any{"kind": "human"},
		},
	}
	msgs := parseClaudeEntry(human, map[string]string{})
	if len(msgs) != 1 || msgs[0]["type"] != "user" || msgs[0]["content"] != "hmm, maybe thats not true" {
		t.Fatalf("queued human prompt = %v, want one user message", msgs)
	}
	if msgs[0]["timestamp"] != "2026-09-22T04:46:34.764Z" {
		t.Fatalf("timestamp = %v", msgs[0]["timestamp"])
	}

	// Commands the system queued, and other attachments, stay hidden.
	for _, att := range []map[string]any{
		{"type": "queued_command", "prompt": "<task-notification>done</task-notification>", "commandMode": "task-notification", "origin": map[string]any{"kind": "task-notification"}},
		{"type": "queued_command", "prompt": "x", "origin": map[string]any{"kind": "coordinator"}},
		{"type": "skill_listing", "content": "skills"},
	} {
		if got := parseClaudeEntry(map[string]any{"type": "attachment", "attachment": att}, map[string]string{}); got != nil {
			t.Fatalf("attachment %v parsed as %v, want nothing", att, got)
		}
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

func TestReadFrom_ConsumesOnlyCompleteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	full := `{"type":"user","message":{"role":"user","content":"first"},"timestamp":"2026-09-21T11:00:00Z"}` + "\n"
	partial := `{"type":"user","message":{"role":"user","content":"sec`
	if err := os.WriteFile(path, []byte(full+partial), 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, off, err := ReadFrom(path, "claude", 0)
	if err != nil || len(msgs) != 1 || msgs[0]["content"] != "first" {
		t.Fatalf("first read: msgs=%v err=%v", msgs, err)
	}
	if off != int64(len(full)) {
		t.Fatalf("offset = %d, want %d: the half-written line waits", off, len(full))
	}

	if err := os.WriteFile(path, []byte(full+partial+`ond"},"timestamp":"2026-09-21T11:00:01Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, off2, err := ReadFrom(path, "claude", off)
	if err != nil || len(msgs) != 1 || msgs[0]["content"] != "second" {
		t.Fatalf("second read: msgs=%v err=%v", msgs, err)
	}

	msgs, _, err = ReadFrom(path, "claude", off2+1000)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("an offset past the end starts over: msgs=%v err=%v", msgs, err)
	}
}

func TestParseAgyEntry_UserInput(t *testing.T) {
	entry := map[string]any{
		"step_index": float64(0),
		"type":       "USER_INPUT",
		"content":    "Fix the bug in auth",
		"created_at": "2026-09-26T12:00:00Z",
	}
	tools := make(map[string]string)
	msgs := parseAgyEntry(entry, tools)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m["type"] != "user" || m["content"] != "Fix the bug in auth" || m["timestamp"] != "2026-09-26T12:00:00Z" {
		t.Errorf("unexpected message: %+v", m)
	}
}

func TestParseAgyEntry_PlannerResponseWithTools(t *testing.T) {
	entry := map[string]any{
		"step_index": float64(1),
		"type":       "PLANNER_RESPONSE",
		"content":    "Looking at files",
		"created_at": "2026-09-26T12:00:05Z",
		"tool_calls": []any{
			map[string]any{
				"name": "view_file",
				"args": map[string]any{"path": "auth.go"},
			},
		},
	}
	tools := make(map[string]string)
	msgs := parseAgyEntry(entry, tools)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m["type"] != "assistant" || m["content"] != "Looking at files" {
		t.Errorf("unexpected message: %+v", m)
	}
	toolUses, ok := m["tool_uses"].([]map[string]any)
	if !ok || len(toolUses) != 1 {
		t.Fatalf("expected 1 tool use, got %+v", m["tool_uses"])
	}
	if toolUses[0]["name"] != "view_file" {
		t.Errorf("expected view_file, got %v", toolUses[0]["name"])
	}
}

func TestParseAgyEntry_GenericToolResult(t *testing.T) {
	entry := map[string]any{
		"step_index": float64(2),
		"type":       "GENERIC",
		"content":    "file content here",
		"created_at": "2026-09-26T12:00:06Z",
	}
	tools := make(map[string]string)
	msgs := parseAgyEntry(entry, tools)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m["type"] != "tool_result" || m["content"] != "file content here" {
		t.Errorf("unexpected message: %+v", m)
	}
}

func TestResolveAgyTranscript(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ANTIGRAVITY_DATA_DIR", tmpDir)

	convDir := filepath.Join(tmpDir, "conv-123", ".system_generated", "logs")
	if err := os.MkdirAll(convDir, 0755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(convDir, "transcript.jsonl")
	if err := os.WriteFile(logFile, []byte(`{"step_index":0,"type":"USER_INPUT","content":"hi"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	found := resolveAgyTranscript("conv-123")
	if found != logFile {
		t.Errorf("expected %q, got %q", logFile, found)
	}

	foundPath := resolveTranscriptPath("conv-123", "", at.Agy)
	if foundPath != logFile {
		t.Errorf("expected resolveTranscriptPath to return %q, got %q", logFile, foundPath)
	}
}

func TestParseAgyEntry_UserInputCleaning(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "wrapped in USER_REQUEST with metadata",
			content: `<USER_REQUEST>
Fix the bug in auth
</USER_REQUEST>
<ADDITIONAL_METADATA>
The current local time is: 2026-09-26T08:03:43-07:00.
</ADDITIONAL_METADATA>
<USER_SETTINGS_CHANGE>
Setting changed
</USER_SETTINGS_CHANGE>`,
			want: "Fix the bug in auth",
		},
		{
			name: "coral session marker and metadata",
			content: `Coral session metadata:
[CORAL_SESSION_ID: session-xyz-123]
This metadata is for Coral bookkeeping only. Do not mention it to the user.

Refactor the database queries`,
			want: "Refactor the database queries",
		},
		{
			name: "only metadata and context summary returns empty",
			content: `<CONTEXT_SUMMARY>
Summary of conversation
</CONTEXT_SUMMARY>
<ADDITIONAL_METADATA>
Time metadata
</ADDITIONAL_METADATA>`,
			want: "",
		},
		{
			name: "plan mode prefix on task reminder",
			content: `<USER_REQUEST>
/plan [Task #978 reminder] SEO and competitor growth audit — assigned to you and waiting. Run 'coral-board task claim' to start.
</USER_REQUEST>
<ADDITIONAL_METADATA>
The current local time is: 2026-09-27T19:50:21-07:00.
</ADDITIONAL_METADATA>`,
			want: "[Task #978 reminder] SEO and competitor growth audit — assigned to you and waiting. Run 'coral-board task claim' to start.",
		},
		{
			name: "goal mode prefix on board unread nudge",
			content: `<USER_REQUEST>
/goal You have 2 unread messages on the message board. Run 'coral-board read' to see them.
</USER_REQUEST>`,
			want: "You have 2 unread messages on the message board. Run 'coral-board read' to see them.",
		},
		{
			name: "plan mode prefix on wait resolved",
			content: `/plan [Wait resolved] Task #42 (DB Migration) is now completed. Run 'coral-board task detail 42' to review.`,
			want: "[Wait resolved] Task #42 (DB Migration) is now completed. Run 'coral-board task detail 42' to review.",
		},
		{
			name: "regular user plan slash command is preserved",
			content: `<USER_REQUEST>
/plan Please design the new database schema
</USER_REQUEST>`,
			want: "/plan Please design the new database schema",
		},
		{
			name: "plan mode prefix on mention-tagged task available",
			content: `/plan @QA Engineer You have tasks available — run 'coral-board task claim' to start`,
			want: "@QA Engineer You have tasks available — run 'coral-board task claim' to start",
		},
		{
			name: "plan mode prefix on personal task notification",
			content: `/plan You have a new task in Coral (#42: Review PR). Claim it with ` + "`coral-agent task claim`" + ` to see the details, then run ` + "`coral-agent task complete 42`" + ` when it's done.`,
			want: "You have a new task in Coral (#42: Review PR). Claim it with `coral-agent task claim` to see the details, then run `coral-agent task complete 42` when it's done.",
		},
		{
			name: "plan mode prefix on coral UI action",
			content: `/plan [Coral UI action #1] A user responded to panel p1 (revision 1).`,
			want: "[Coral UI action #1] A user responded to panel p1 (revision 1).",
		},
		{
			name: "multiple slash prefixes on board unread reminder",
			content: `/plan /goal You have 1 unread message on the message board. Run 'coral-board read' to see them.`,
			want: "You have 1 unread message on the message board. Run 'coral-board read' to see them.",
		},
	}

	tools := make(map[string]string)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := map[string]any{
				"step_index": float64(0),
				"type":       "USER_INPUT",
				"content":    tt.content,
				"created_at": "2026-09-26T12:00:00Z",
			}
			msgs := parseAgyEntry(entry, tools)
			if tt.want == "" {
				if len(msgs) != 0 {
					t.Fatalf("expected message to be filtered out, got %+v", msgs)
				}
				return
			}
			if len(msgs) != 1 {
				t.Fatalf("expected 1 message, got %d", len(msgs))
			}
			if msgs[0]["content"] != tt.want {
				t.Errorf("got %q, want %q", msgs[0]["content"], tt.want)
			}
		})
	}
}

func TestParseAgyEntry_RichToolCallsAndResultLinking(t *testing.T) {
	tools := make(map[string]string)

	// Step 1: run_command with escaped quotes in args
	step1 := map[string]any{
		"step_index": float64(1),
		"type":       "PLANNER_RESPONSE",
		"content":    "Running command",
		"created_at": "2026-09-26T12:00:01Z",
		"tool_calls": []any{
			map[string]any{
				"name": "run_command",
				"args": map[string]any{
					"CommandLine": `"git status"`,
					"toolAction":  `"Checking git status"`,
					"toolSummary": `"Git status check"`,
				},
			},
		},
	}
	msgs1 := parseAgyEntry(step1, tools)
	if len(msgs1) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs1))
	}
	tu1 := msgs1[0]["tool_uses"].([]map[string]any)
	if len(tu1) != 1 {
		t.Fatalf("expected 1 tool_use, got %d", len(tu1))
	}
	if tu1[0]["command"] != "git status" {
		t.Errorf("expected command 'git status', got %v", tu1[0]["command"])
	}
	if tu1[0]["description"] != "Checking git status" {
		t.Errorf("expected description 'Checking git status', got %v", tu1[0]["description"])
	}
	if tu1[0]["tool_use_id"] != "step-1-0" {
		t.Errorf("expected call ID 'step-1-0', got %v", tu1[0]["tool_use_id"])
	}

	// Step 2: GENERIC tool result linking to step 1
	step2 := map[string]any{
		"step_index": float64(2),
		"type":       "GENERIC",
		"content":    "On branch main",
		"created_at": "2026-09-26T12:00:02Z",
		"status":     "DONE",
	}
	msgs2 := parseAgyEntry(step2, tools)
	if len(msgs2) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs2))
	}
	if msgs2[0]["type"] != "tool_result" {
		t.Errorf("expected tool_result, got %v", msgs2[0]["type"])
	}
	if msgs2[0]["tool_use_id"] != "step-1-0" {
		t.Errorf("expected tool_use_id 'step-1-0', got %v", msgs2[0]["tool_use_id"])
	}
	if msgs2[0]["tool_name"] != "run_command" {
		t.Errorf("expected tool_name 'run_command', got %v", msgs2[0]["tool_name"])
	}
	if msgs2[0]["is_error"] != false {
		t.Errorf("expected is_error false, got %v", msgs2[0]["is_error"])
	}

	// Step 3: replace_file_content
	step3 := map[string]any{
		"step_index": float64(3),
		"type":       "PLANNER_RESPONSE",
		"created_at": "2026-09-26T12:00:03Z",
		"tool_calls": []any{
			map[string]any{
				"name": "replace_file_content",
				"args": map[string]any{
					"TargetFile":         `"/src/main.go"`,
					"TargetContent":      `"old code"`,
					"ReplacementContent": `"new code"`,
					"toolAction":         `"Editing main.go"`,
				},
			},
		},
	}
	msgs3 := parseAgyEntry(step3, tools)
	tu3 := msgs3[0]["tool_uses"].([]map[string]any)
	if tu3[0]["old_string"] != "old code" || tu3[0]["new_string"] != "new code" {
		t.Errorf("expected diff strings, got old=%v new=%v", tu3[0]["old_string"], tu3[0]["new_string"])
	}
	if tu3[0]["input_summary"] != "/src/main.go" {
		t.Errorf("expected TargetFile as input_summary, got %v", tu3[0]["input_summary"])
	}

	// Step 4: GENERIC tool result with ERROR status
	step4 := map[string]any{
		"step_index": float64(4),
		"type":       "GENERIC",
		"content":    "file not found",
		"created_at": "2026-09-26T12:00:04Z",
		"status":     "ERROR",
	}
	msgs4 := parseAgyEntry(step4, tools)
	if msgs4[0]["tool_use_id"] != "step-3-0" || msgs4[0]["tool_name"] != "replace_file_content" {
		t.Errorf("expected step-3-0 and replace_file_content, got %v / %v", msgs4[0]["tool_use_id"], msgs4[0]["tool_name"])
	}
	if msgs4[0]["is_error"] != true {
		t.Errorf("expected is_error true, got %v", msgs4[0]["is_error"])
	}

	// Step 5: ask_question
	step5 := map[string]any{
		"step_index": float64(5),
		"type":       "PLANNER_RESPONSE",
		"created_at": "2026-09-26T12:00:05Z",
		"tool_calls": []any{
			map[string]any{
				"name": "ask_question",
				"args": map[string]any{
					"questions": []any{
						map[string]any{
							"question": "Which database?",
							"options":  []any{"PostgreSQL", "SQLite"},
						},
					},
				},
			},
		},
	}
	msgs5 := parseAgyEntry(step5, tools)
	tu5 := msgs5[0]["tool_uses"].([]map[string]any)
	if tu5[0]["questions"] == nil {
		t.Fatalf("expected questions to be populated, got nil")
	}

	// Step 6: write_to_file
	step6 := map[string]any{
		"step_index": float64(6),
		"type":       "PLANNER_RESPONSE",
		"created_at": "2026-09-26T12:00:06Z",
		"tool_calls": []any{
			map[string]any{
				"name": "write_to_file",
				"args": map[string]any{
					"TargetFile":  `"/src/new.go"`,
					"CodeContent": `"package main\n\nfunc main() {}"`,
				},
			},
		},
	}
	msgs6 := parseAgyEntry(step6, tools)
	tu6 := msgs6[0]["tool_uses"].([]map[string]any)
	if tu6[0]["write_content"] != "package main\n\nfunc main() {}" {
		t.Errorf("expected write_content, got %v", tu6[0]["write_content"])
	}
}

func TestResolveAgyTranscript_MarkerAndSorting(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ANTIGRAVITY_DATA_DIR", tmpDir)

	uuid := "a1b2c3d4-e5f6-7890-abcd-ef1234567890"

	// Create older session directory
	oldDir := filepath.Join(tmpDir, "older-conv", ".system_generated", "logs")
	os.MkdirAll(oldDir, 0755)
	os.WriteFile(filepath.Join(oldDir, "transcript.jsonl"), []byte(`{"step_index":0,"type":"USER_INPUT","content":"older"}`+"\n"), 0644)

	// Create newer session directory containing embedded coral session ID
	newDir := filepath.Join(tmpDir, "newer-conv", ".system_generated", "logs")
	os.MkdirAll(newDir, 0755)
	newTranscript := filepath.Join(newDir, "transcript.jsonl")
	os.WriteFile(newTranscript, []byte(fmt.Sprintf(`{"step_index":0,"type":"USER_INPUT","content":"Coral session metadata:\n%s %s\nDo not mention.\n\nWork"}`+"\n", at.CoralSessionMarkerPrefix, uuid)), 0644)

	found := resolveAgyTranscript(uuid)
	if found != newTranscript {
		t.Fatalf("expected %q, got %q", newTranscript, found)
	}
}

func TestResolveAgyTranscript_FromHistory(t *testing.T) {
	tmpDir := t.TempDir()
	// Set ANTIGRAVITY_DATA_DIR to brain subdirectory under tmpDir
	brainDir := filepath.Join(tmpDir, "brain")
	t.Setenv("ANTIGRAVITY_DATA_DIR", brainDir)

	convID := "c1d2e3f4-a5b6-7890-1234-567890abcdef"
	convDir := filepath.Join(brainDir, convID, ".system_generated", "logs")
	os.MkdirAll(convDir, 0755)
	targetTranscript := filepath.Join(convDir, "transcript.jsonl")
	os.WriteFile(targetTranscript, []byte(`{"step_index":0,"type":"USER_INPUT","content":"hello"}`+"\n"), 0644)

	// Create history.jsonl in tmpDir
	ws := "/workspace/myproject"
	historyPath := filepath.Join(tmpDir, "history.jsonl")
	os.WriteFile(historyPath, []byte(fmt.Sprintf(`{"display":"hi","workspace":%q,"conversationId":%q}`+"\n", ws, convID)), 0644)

	found := resolveAgyTranscript("arbitrary-coral-id", ws)
	if found != targetTranscript {
		t.Fatalf("expected %q, got %q", targetTranscript, found)
	}

	foundViaResolve := resolveTranscriptPath("arbitrary-coral-id", ws, at.Agy)
	if foundViaResolve != targetTranscript {
		t.Fatalf("expected %q, got %q", targetTranscript, foundViaResolve)
	}
}

func TestResolveAgyTranscript_ExplicitCoralIDRejectsWorkspaceFallbackUntilExactTranscriptAppears(t *testing.T) {
	tmpDir := t.TempDir()
	brainDir := filepath.Join(tmpDir, "brain")
	t.Setenv("ANTIGRAVITY_DATA_DIR", brainDir)
	ws := "/workspace/shared"
	coralID := "14c7245b-9fb4-67de-871c-a03f6186ed30"
	oldID := "old-conversation"
	oldDir := filepath.Join(brainDir, oldID, ".system_generated", "logs")
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "transcript.jsonl"), []byte(`{"step_index":0,"type":"USER_INPUT","content":"old"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(tmpDir, "history.jsonl")
	if err := os.WriteFile(historyPath, []byte(fmt.Sprintf(`{"workspace":%q,"conversationId":%q}`+"\n", ws, oldID)), 0644); err != nil {
		t.Fatal(err)
	}

	// The fresh transcript is not present yet. A same-workspace history row
	// must not be bound to the new live Coral session.
	if got := resolveAgyTranscriptStrict(coralID, ws); got != "" {
		t.Fatalf("resolved unrelated workspace transcript: %q", got)
	}

	newDir := filepath.Join(brainDir, "new-conversation", ".system_generated", "logs")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(newDir, "transcript.jsonl")
	content := fmt.Sprintf(`{"step_index":0,"type":"USER_INPUT","content":"%s %s fresh"}`+"\n", at.CoralSessionMarkerPrefix, coralID)
	if err := os.WriteFile(newPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if got := resolveAgyTranscriptStrict(coralID, ws); got != newPath {
		t.Fatalf("resolved %q, want exact transcript %q", got, newPath)
	}
}

func TestSessionReader_AgyDoesNotKeepPoisonedPath(t *testing.T) {
	tmpDir := t.TempDir()
	brainDir := filepath.Join(tmpDir, "brain")
	t.Setenv("ANTIGRAVITY_DATA_DIR", brainDir)
	coralID := "14c7245b-9fb4-67de-871c-a03f6186ed30"
	path := filepath.Join(brainDir, coralID, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"step_index":0,"type":"USER_INPUT","content":"unrelated"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewSessionReader()
	if _, total := r.ReadNewMessages(coralID, "/workspace/shared", at.Agy); total != 1 {
		t.Fatalf("failed to seed stale cache, total=%d", total)
	}
	c := r.cache[coralID]
	c.toolUseNames["stale-tool"] = "Read"
	if c.offset == 0 || len(c.messages) == 0 {
		t.Fatal("stale cache was not populated")
	}
	if _, total := r.ReadNewMessagesForLive(coralID, "/workspace/shared", at.Agy); total != 0 {
		t.Fatalf("strict live read retained unrelated transcript, total=%d", total)
	}
	if len(c.messages) != 0 || c.offset != 0 || len(c.toolUseNames) != 0 {
		t.Fatalf("poisoned cache was not cleared: %#v", c)
	}
	content := fmt.Sprintf(`{"step_index":0,"type":"USER_INPUT","content":"%s %s fresh"}`+"\n", at.CoralSessionMarkerPrefix, coralID)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	msgs, total := r.ReadNewMessagesForLive(coralID, "/workspace/shared", at.Agy)
	if total != 1 || len(msgs) != 1 || !strings.Contains(msgs[0]["content"].(string), "fresh") {
		t.Fatalf("expected exact transcript to be discovered after replacement, total=%d msgs=%#v", total, msgs)
	}
}
