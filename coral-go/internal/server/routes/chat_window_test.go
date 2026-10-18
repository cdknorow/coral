package routes

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/jsonl"
)

var keyRe = regexp.MustCompile(`"key":"[0-9a-f]{20}"`)

// chatAnswers asks the live chat route a set of questions about one transcript and returns each
// answer as the bytes the server sent: pages (the newest, an offset, a page above a cursor, one
// that reaches the very first message), polls from a position (before and inside the window),
// and key lookups for an early, a middle and a late message.
func chatAnswers(t *testing.T, sid string) map[string]string {
	t.Helper()
	server, _, _, _ := setupSessionsTestServer(t)
	get := func(query string) string {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("%s/api/sessions/live/claude-%s/chat?session_id=%s&%s", server.URL, sid, sid, query))
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
		return string(b)
	}
	// The keys are this server's own (each has its secret): looked up from its own full page, and
	// written as K in the answers, so two servers' answers compare.
	var all struct {
		Messages []map[string]any `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(get("after=0&limit=1000")), &all))
	require.Len(t, all.Messages, 96)
	keyOf := func(i int) string { k, _ := all.Messages[i]["key"].(string); return k }
	out := map[string]string{}
	for _, q := range []string{
		"after=0", "after=0&limit=7", "after=0&limit=7&offset=3", "after=0&limit=10&before=23", "after=0&limit=10&before=9",
		"after=0&limit=500&before=60", "after=0&limit=5&before=3", "after=0&limit=4&before=0",
		"after=0&limit=15&offset=70", "after=0&limit=1000",
		"after=5", "after=40", "after=95", "after=96", "after=300", "after=0&limit=6&trim=20",
	} {
		out[q] = keyRe.ReplaceAllString(get(q), `"key":"K"`)
	}
	for _, i := range []int{0, 1, 17, 48, 95} {
		out[fmt.Sprintf("find_key #%d", i)] = get("find_key=" + keyOf(i))
	}
	return out
}

// #4620: the chat answers the same whether the reader holds the whole transcript or only a small
// window of it and reads the rest from the file: pages, the total, polls, and where a saved
// message is, byte for byte.
func TestChatAnswersAreTheSameWithASmallTranscriptWindow(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-0000-0000-000000004620"
	var b strings.Builder
	for i := 0; i < 32; i++ { // 96 messages: a user turn, an assistant turn with a tool call, its result
		ts := fmt.Sprintf("2026-10-18T09:%02d:%02dZ", i/60, i%60)
		fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"content":"question %d"}}`+"\n", ts, i)
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"text","text":"answer %d"},{"type":"tool_use","id":"tu_%d","name":"Bash","input":{"command":"echo %d"}}]}}`+"\n", ts, i, i, i)
		fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"tu_%d","content":"out %d"}]}}`+"\n", ts, i, i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sid+".jsonl"), []byte(b.String()), 0o644))

	whole := chatAnswers(t, sid)

	restore := jsonl.SetTranscriptBoundsForTest(12, 1<<30, 900, 4096) // a 12-message window, chunks of a few lines
	defer restore()
	windowed := chatAnswers(t, sid)

	require.Equal(t, len(whole), len(windowed))
	for q, want := range whole {
		assert.Equal(t, want, windowed[q], q)
	}
	assert.Contains(t, whole["find_key #17"], `"found":true`)
	assert.Contains(t, whole["find_key #17"], `"index":17`)
}
