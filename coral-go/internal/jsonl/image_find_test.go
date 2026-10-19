package jsonl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agent"
)

func writeLines(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return p
}

func claudeLine(t *testing.T, data string) string {
	b, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}}}}})
	require.NoError(t, err)
	return string(b)
}

func TestFindImageFindsByContentAndRefusesTheRest(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nbody-one")
	other := []byte("\x89PNG\r\n\x1a\nbody-two")
	p := writeLines(t, `{"type":"user","message":{"content":"no image on this line"}}`, claudeLine(t, base64.StdEncoding.EncodeToString(other)), claudeLine(t, base64.StdEncoding.EncodeToString(png)))
	got, err := FindImage(context.Background(), p, "claude", agent.ImageRef(base64.StdEncoding.EncodeToString(png)))
	require.NoError(t, err)
	assert.Equal(t, png, got)
	_, err = FindImage(context.Background(), p, "claude", agent.ImageRef("not in the file"))
	assert.ErrorIs(t, err, ErrImageNotFound)
	for _, bad := range []string{"", "x", "../../etc/passwd", strings.Repeat("Z", 32)} {
		_, err = FindImage(context.Background(), p, "claude", bad)
		assert.ErrorIs(t, err, ErrImageNotFound, bad)
	}
	_, err = FindImage(context.Background(), filepath.Join(t.TempDir(), "none.jsonl"), "claude", agent.ImageRef("x"))
	assert.Error(t, err)
}

func TestFindImageRefusesAnOversizedImageAndSkipsAnOverlongLine(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(make([]byte, 6000))
	p := writeLines(t, claudeLine(t, big))
	restoreImg := maxImageBytes
	maxImageBytes = 5000
	t.Cleanup(func() { maxImageBytes = restoreImg })
	_, err := FindImage(context.Background(), p, "claude", agent.ImageRef(big))
	assert.ErrorIs(t, err, ErrImageTooLarge, "decoded size over the cap")

	maxImageBytes = restoreImg
	restoreLine := maxImageLine
	maxImageLine = 4000
	t.Cleanup(func() { maxImageLine = restoreLine })
	_, err = FindImage(context.Background(), p, "claude", agent.ImageRef(big))
	assert.ErrorIs(t, err, ErrImageNotFound, "a line over the cap is not read")
}

func TestFindImageFindsACodexDataURLAndTheEventsImageList(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\ncodex")
	enc := base64.StdEncoding.EncodeToString(png)
	resp, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + enc}}}})
	ev, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "m", "images": []any{"data:image/png;base64," + enc}}})
	for _, line := range []string{string(resp), string(ev)} {
		got, err := FindImage(context.Background(), writeLines(t, line), "claude", agent.ImageRef(enc)) // the walk is the same for any agent type; the Codex rollout open is covered by the route test
		require.NoError(t, err)
		assert.Equal(t, png, got)
	}
}

// The reader's messages carry the reference, not the bytes; a message that is only images reads as its labels.
func TestClaudeImageBlocksBecomeChipsWithReferences(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nx"))
	p := writeLines(t, claudeLine(t, enc))
	msgs, _, err := ReadFromContext(context.Background(), p, "claude", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "[Image #1]", msgs[0]["content"])
	atts, _ := msgs[0]["attachments"].([]map[string]any)
	require.Len(t, atts, 1)
	assert.Equal(t, agent.ImageRef(enc), atts[0]["ref"])
	b, _ := json.Marshal(msgs[0])
	assert.NotContains(t, string(b), enc)
}
