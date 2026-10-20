package routes

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5400: the dashboard types "<path> coral://artifacts/<id>" into the agent for a pasted image, and <path> is an absolute path in this computer's uploads folder,
// which sits under the home directory and so names the OS account. That text comes back in the chat (a "text" or "content" field, which is free text and shown as
// written), so a Coral Hub viewer read the account name there. The uploads folder, wherever it is under the home directory, is shown as "~/..." in every answer
// a hub gets, free text included; other text is still shown as written.
func TestAPastedImagesPathInTheChatShowsAsHomeThroughTheHub(t *testing.T) {
	home, real := hubTestHome(t)
	old, oldCoral := uploadDir, uploadCoralDir
	t.Cleanup(func() { uploadDir, uploadCoralDir = old, oldCoral })
	InitUploadDir(filepath.Join(home, ".coral"))
	id := strings.Repeat("ab", 32)
	pasted := filepath.ToSlash(filepath.Join(home, ".coral", "uploads", "screenshot_20261020_103000_ab12.png")) + " coral://artifacts/" + id
	resolved := filepath.ToSlash(filepath.Join(real, ".coral", "uploads", "image_cd34.png")) + " coral://artifacts/" + id
	body, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "text": pasted, "ts": "2026-10-20T10:30:00Z"},
			map[string]any{"role": "user", "content": "look at this: " + resolved + " and tell me what you see"},
			map[string]any{"role": "assistant", "summary": "read " + pasted, "output": "cat " + pasted},
			map[string]any{"role": "user", "message": pasted, "line": pasted},
		},
		"note": "cd " + filepath.ToSlash(filepath.Join(home, "proj")) + " && make", // other text keeps its home path as written, as before
	})
	require.NoError(t, err)
	out, ok := hubJSON(body)
	require.True(t, ok)
	s := string(out)
	for _, h := range []string{home, real} {
		assert.NotContains(t, strings.ReplaceAll(s, filepath.ToSlash(filepath.Join(home, "proj")), ""), strings.TrimPrefix(h, "/"), "no home path in the uploads text: %s", s)
	}
	var got struct {
		Messages []map[string]string `json:"messages"`
		Note     string              `json:"note"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "~/.coral/uploads/screenshot_20261020_103000_ab12.png coral://artifacts/"+id, got.Messages[0]["text"])
	assert.Equal(t, "look at this: ~/.coral/uploads/image_cd34.png coral://artifacts/"+id+" and tell me what you see", got.Messages[1]["content"])
	assert.Equal(t, "read ~/.coral/uploads/screenshot_20261020_103000_ab12.png coral://artifacts/"+id, got.Messages[2]["summary"])
	assert.Equal(t, "cat ~/.coral/uploads/screenshot_20261020_103000_ab12.png coral://artifacts/"+id, got.Messages[2]["output"])
	assert.Equal(t, "~/.coral/uploads/screenshot_20261020_103000_ab12.png coral://artifacts/"+id, got.Messages[3]["message"])
	assert.Equal(t, "~/.coral/uploads/screenshot_20261020_103000_ab12.png coral://artifacts/"+id, got.Messages[3]["line"])
	assert.Contains(t, got.Note, filepath.ToSlash(filepath.Join(home, "proj")), "free text that is not about the uploads folder is shown as written (#3559)")

	// A Coral whose data folder is not under the home directory has nothing to hide, and nothing changes.
	InitUploadDir(t.TempDir())
	plain := filepath.ToSlash(uploadDir) + "/x.png coral://artifacts/" + id
	b2, err := json.Marshal(map[string]any{"text": plain})
	require.NoError(t, err)
	out2, ok := hubJSON(b2)
	require.True(t, ok)
	var g2 map[string]string
	require.NoError(t, json.Unmarshal(out2, &g2))
	assert.Equal(t, plain, g2["text"])
}
