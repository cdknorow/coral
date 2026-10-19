package jsonl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakePNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="

func codexImageRollout(t *testing.T, lines ...map[string]any) []map[string]any {
	t.Helper()
	dir := t.TempDir()
	sessionID := "019e90eb-a08a-7511-a410-23e7ae3e7777"
	codexHome := filepath.Join(dir, ".codex")
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "10", "19")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	var b strings.Builder
	for _, l := range lines {
		raw, _ := json.Marshal(l)
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "rollout-2026-10-19T10-00-00-"+sessionID+".jsonl"), []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	msgs, _ := NewSessionReader().ReadNewMessages(sessionID, "", "codex")
	return msgs
}

func codexImageItem(ts string, blocks ...any) map[string]any {
	return map[string]any{"timestamp": ts, "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": blocks}}
}

func imageBlocks(n int, path string) []any {
	open := `<image name=[Image #` + string(rune('0'+n)) + `]`
	if path != "" {
		open += ` path="` + path + `"`
	}
	open += `>`
	return []any{
		map[string]any{"type": "input_text", "text": open},
		map[string]any{"type": "input_image", "image_url": fakePNG},
		map[string]any{"type": "input_text", "text": "</image>"},
	}
}

func assertNoRawImage(t *testing.T, msgs []map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(msgs)
	for _, bad := range []string{"base64", "data:image", "<image", "</image>", "/Users/"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("message carries %q: %s", bad, raw)
		}
	}
}

func attachmentsOf(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	atts, _ := m["attachments"].([]map[string]any)
	return atts
}

func TestCodexPastedImageBecomesAttachment(t *testing.T) {
	one := imageBlocks(1, "/Users/someone/.codex/attachments/shot1.png")
	two := append(imageBlocks(1, "/Users/someone/.codex/attachments/a.png"), imageBlocks(2, "/Users/someone/.codex/attachments/b.png")...)
	around := append(append([]any{map[string]any{"type": "input_text", "text": "look at"}}, imageBlocks(1, "/Users/someone/x/c.png")...),
		map[string]any{"type": "input_text", "text": "and tell me why"})
	msgs := codexImageRollout(t,
		codexImageItem("2026-10-19T10:00:01.000Z", one...),
		codexImageItem("2026-10-19T10:00:10.000Z", two...),
		codexImageItem("2026-10-19T10:00:20.000Z", around...),
		codexImageItem("2026-10-19T10:00:30.000Z", imageBlocks(1, "")...),
	)
	if len(msgs) != 4 {
		t.Fatalf("want 4 messages, got %d: %#v", len(msgs), msgs)
	}
	assertNoRawImage(t, msgs)

	want := []struct {
		content string
		names   []string
		files   []string
	}{
		{"[Image #1]", []string{"[Image #1]"}, []string{"shot1.png"}},
		{"[Image #1] [Image #2]", []string{"[Image #1]", "[Image #2]"}, []string{"a.png", "b.png"}},
		{"look at\nand tell me why", []string{"[Image #1]"}, []string{"c.png"}},
		{"[Image #1]", []string{"[Image #1]"}, []string{""}}, // the file is missing: a name, no file
	}
	for i, w := range want {
		if msgs[i]["content"] != w.content {
			t.Errorf("msg %d content = %q, want %q", i, msgs[i]["content"], w.content)
		}
		atts := attachmentsOf(t, msgs[i])
		if len(atts) != len(w.names) {
			t.Fatalf("msg %d attachments = %#v", i, atts)
		}
		for j := range atts {
			if atts[j]["name"] != w.names[j] || atts[j]["file"] != w.files[j] {
				t.Errorf("msg %d attachment %d = %#v, want %s / %s", i, j, atts[j], w.names[j], w.files[j])
			}
		}
	}
}

func TestCodexPastedImageEventAndResponseCopiesMerge(t *testing.T) {
	event := map[string]any{"timestamp": "2026-10-19T10:00:01.000Z", "type": "event_msg",
		"payload": map[string]any{"type": "user_message", "message": "", "images": []any{fakePNG}}}
	msgs := codexImageRollout(t, event,
		codexImageItem("2026-10-19T10:00:01.050Z", imageBlocks(1, "/Users/someone/.codex/attachments/shot.png")...))
	if len(msgs) != 1 {
		t.Fatalf("copies did not merge: %#v", msgs)
	}
	assertNoRawImage(t, msgs)
	atts := attachmentsOf(t, msgs[0])
	if msgs[0]["content"] != "[Image #1]" || len(atts) != 1 || atts[0]["name"] != "[Image #1]" || atts[0]["file"] != "shot.png" {
		t.Fatalf("unexpected merged message: %#v", msgs[0])
	}
}
