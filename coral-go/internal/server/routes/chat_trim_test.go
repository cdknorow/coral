package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3723: a chat page asked for with ?trim=N carries at most N bytes of each long tool field,
// marked trimmed; the same message asked for alone without trim is whole, and the transcript
// reader's shared messages are never changed by a trimmed page.
func TestSessionsChat_TrimShortensLongToolFields(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	sid := "00000000-0000-0000-0000-000000000323"
	// é is two bytes: a cut at an odd byte count would split one.
	long := strings.Repeat("é", 2500)              // 5000 bytes
	edit := strings.Repeat("old line\n", 400)      // 3600 bytes
	write := strings.Repeat("written line\n", 300) // 3900 bytes
	line := func(v any) string { b, _ := json.Marshal(v); return string(b) + "\n" }
	transcript := line(map[string]any{"type": "user", "message": map[string]any{"content": "please edit"}}) +
		line(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "Editing."},
			map[string]any{"type": "tool_use", "id": "t-edit", "name": "Edit", "input": map[string]any{"file_path": "a.go", "old_string": edit, "new_string": "new"}},
			map[string]any{"type": "tool_use", "id": "t-write", "name": "Write", "input": map[string]any{"file_path": "b.go", "content": write}},
			map[string]any{"type": "tool_use", "id": "t-bash", "name": "Bash", "input": map[string]any{"command": "ls"}},
		}}}) +
		line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t-bash", "content": long},
		}}}) +
		line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t-edit", "content": "short output"},
		}}})
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sid+".jsonl"), []byte(transcript), 0644))

	get := func(query string) []map[string]any {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("%s/api/sessions/live/claude-%s/chat?session_id=%s&after=0&%s", server.URL, sid, sid, query))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body.Messages
	}
	tools := func(m map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, x := range m["tool_uses"].([]any) {
			tm := x.(map[string]any)
			out[tm["name"].(string)] = tm
		}
		return out
	}

	whole := get("limit=30")
	require.Len(t, whole, 4)
	assert.Nil(t, whole[1]["trimmed"], "without trim nothing is marked")
	assert.Equal(t, long, whole[2]["content"])

	page := get("limit=30&trim=301")
	require.Len(t, page, 4)
	assert.Nil(t, page[0]["trimmed"], "a message with no long field is unmarked")
	assert.Equal(t, true, page[1]["trimmed"], "a message with a shortened tool is marked")
	tl := tools(page[1])
	// Cut at or before 301 bytes, never inside a word (a token could be one).
	cutBefore := func(field, whole string) {
		t.Helper()
		assert.LessOrEqual(t, len(field), 301)
		assert.Greater(t, len(field), 280)
		assert.True(t, strings.HasPrefix(whole, field))
		letter := func(c byte) bool { return c >= 'a' && c <= 'z' }
		assert.False(t, letter(field[len(field)-1]) && letter(whole[len(field)]), "not inside a word: %q|%q", field[len(field)-5:], whole[len(field):len(field)+5])
	}
	cutBefore(tl["Edit"]["old_string"].(string), edit)
	assert.Equal(t, "new", tl["Edit"]["new_string"], "a short field is kept")
	assert.Equal(t, true, tl["Edit"]["trimmed"])
	cutBefore(tl["Write"]["write_content"].(string), write)
	assert.Equal(t, true, tl["Write"]["trimmed"])
	assert.Nil(t, tl["Bash"]["trimmed"], "a tool with nothing long is unmarked")
	assert.Equal(t, "Editing.", page[1]["text"], "the message's own text is never shortened")
	out := page[2]["content"].(string)
	assert.Equal(t, true, page[2]["trimmed"])
	assert.LessOrEqual(t, len(out), 301)
	assert.Greater(t, len(out), 298)
	assert.True(t, utf8.ValidString(out), "cut at a character boundary")
	assert.True(t, strings.HasPrefix(long, out))
	assert.Nil(t, page[3]["trimmed"], "a short output is unmarked")
	assert.Equal(t, "short output", page[3]["content"])

	// The card's full text: that one message, alone, without trim.
	one := get("limit=1&before=3")
	require.Len(t, one, 1)
	assert.Equal(t, long, one[0]["content"])
	assert.Nil(t, one[0]["trimmed"])

	// The reader's messages were not changed by the trimmed page.
	again := get("limit=30")
	assert.Equal(t, long, again[2]["content"])
	assert.Equal(t, edit, tools(again[1])["Edit"]["old_string"])

	// A trim below the floor is raised to it.
	low := get("limit=30&trim=5")
	assert.Len(t, low[2]["content"].(string), trimMin)
}
