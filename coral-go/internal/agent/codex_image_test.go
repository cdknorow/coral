package agent

import (
	"strings"
	"testing"
)

const indexFakePNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="

func TestCodexIndexTextHasNoImageBytes(t *testing.T) {
	blocks := []any{
		map[string]any{"type": "input_text", "text": `<image name=[Image #1] path="/Users/someone/.codex/a.png">`},
		map[string]any{"type": "input_image", "image_url": indexFakePNG},
		map[string]any{"type": "input_text", "text": "</image>"},
	}
	cases := map[string]string{
		"image only":  "[Image #1]",
		"with typing": "why is this red",
	}
	for name, want := range cases {
		content := any(blocks)
		if name == "with typing" {
			content = append(append([]any{}, blocks...), map[string]any{"type": "input_text", "text": want})
		}
		_, text := codexIndexMessage(map[string]any{"role": "user", "content": content})
		if text != want {
			t.Errorf("%s: response_item index text = %q, want %q", name, text, want)
		}
	}
	event := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "", "images": []any{indexFakePNG, "/Users/someone/b.png"}}}
	_, text := codexIndexMessage(event)
	if text != "[Image #1] [Image #2]" || strings.Contains(text, "base64") || strings.Contains(text, "/Users/") {
		t.Errorf("event_msg index text = %q", text)
	}
}
