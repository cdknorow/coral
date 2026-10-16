package jsonl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// #3694: whatever a transcript holds, the chat reply built from it parses, and nothing from a
// line that isn't whole JSON is embedded. Lines: plain text, HTML, invalid UTF-8, a raw control
// character (not valid JSON: skipped), a Write whose content is all quotes, backslashes and
// newlines (truncated to 10KB, cut on a rune boundary, never inside an escape), a multi-byte
// character at the cut, and a partial last line still being written.
func TestChatReplyFromHostileTranscriptParses(t *testing.T) {
	dir := t.TempDir()
	sessionID := "chat-fixture-3694"
	projectDir := filepath.Join(dir, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	line := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	user := func(text string) string {
		return line(map[string]any{"type": "user", "timestamp": "2026-10-16T00:00:00Z", "message": map[string]any{"content": text}})
	}
	escapes := strings.Repeat("\"\\\n", 4000)                               // 12000 bytes of quotes, backslashes and newlines
	multibyte := strings.Repeat("a", 9999) + "é" + strings.Repeat("b", 100) // "é" (2 bytes) straddles the 10000-byte cut
	write := func(content string) string {
		return line(map[string]any{"type": "assistant", "timestamp": "2026-10-16T00:00:01Z", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": "tu", "name": "Write", "input": map[string]any{"file_path": "/r/x.txt", "content": content}},
		}}})
	}
	var b strings.Builder
	b.WriteString(user("plain first message"))
	b.WriteString(user("<img src=x onerror=alert(1)> and </script><script>alert(2)</script>"))
	b.WriteString(`{"type":"user","timestamp":"2026-10-16T00:00:02Z","message":{"content":"bad utf8 ` + "\xff\xfe" + ` here"}}` + "\n")
	b.WriteString(`{"type":"user","timestamp":"2026-10-16T00:00:03Z","message":{"content":"raw control ` + "\x01" + ` here"}}` + "\n")
	b.WriteString(write(escapes))
	b.WriteString(write(multibyte))
	b.WriteString(user("last whole message"))
	b.WriteString(`{"type":"user","timestamp":"2026-10-16T00:00:09Z","message":{"content":"partial line still being wri`)
	if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, _ := NewSessionReader().ReadAllMessagesContext(context.Background(), sessionID, "", "claude")
	reply, err := json.Marshal(map[string]any{"messages": msgs, "total": len(msgs)})
	if err != nil {
		t.Fatalf("the reply must encode: %v", err)
	}
	if !json.Valid(reply) {
		t.Fatalf("the reply must parse: %s", reply)
	}
	s := string(reply)
	if strings.Contains(s, "partial line still") {
		t.Error("a partial last line must not be embedded")
	}
	for _, want := range []string{"plain first message", "last whole message"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	// HTML is text in a JSON string: the encoder escapes its angle brackets.
	if strings.Contains(s, "<img") || !strings.Contains(s, "\\u003cimg src=x onerror=alert(1)\\u003e") {
		t.Error("HTML must reach the reply only as escaped text")
	}
	var parsed struct{ Messages []map[string]any }
	if err := json.Unmarshal(reply, &parsed); err != nil {
		t.Fatal(err)
	}
	var writes []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if wc, ok := x["write_content"].(string); ok {
				writes = append(writes, wc)
			}
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case string:
			if !utf8.ValidString(x) {
				t.Errorf("a string in the reply is not valid UTF-8: %q", x[:min(len(x), 40)])
			}
		}
	}
	for _, m := range parsed.Messages {
		walk(m)
	}
	if len(writes) != 2 {
		t.Fatalf("both Write tool calls are in the reply, got %d", len(writes))
	}
	for _, w := range writes {
		if !strings.HasSuffix(w, "\n... (truncated)") || len(w) > 10000+len("\n... (truncated)") {
			t.Errorf("a long write is truncated to 10KB: %d bytes", len(w))
		}
		if strings.ContainsRune(w, utf8.RuneError) {
			t.Error("the cut must not split a character into U+FFFD")
		}
	}
	if !strings.HasPrefix(writes[0], escapes[:9999]) {
		t.Error("the escape-heavy write keeps its text up to the cut, exactly")
	}
	if !strings.HasPrefix(writes[1], strings.Repeat("a", 9999)+"\n... (truncated)") {
		t.Errorf("the cut moves back before a character it would split: %q", writes[1][9990:min(len(writes[1]), 10020)])
	}
}

// runeCut never splits a UTF-8 sequence, at every offset of a string of mixed widths.
func TestRuneCut(t *testing.T) {
	s := "aé€😀\"\\b"
	for n := 0; n <= len(s)+1; n++ {
		k := runeCut(s, n)
		if k > n || k > len(s) || !utf8.ValidString(s[:k]) {
			t.Fatalf("runeCut(%d) = %d: %q", n, k, s[:min(k, len(s))])
		}
		if n <= len(s) && utf8.ValidString(s[:n]) && k != n {
			t.Fatalf("runeCut(%d) = %d, but %d was a boundary", n, k, n)
		}
	}
}
