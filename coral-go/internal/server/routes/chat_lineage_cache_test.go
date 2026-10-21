package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/transcriptlink"
)

// writeClaudeTranscript writes a Claude transcript of about size bytes (prompts, tool calls with
// large inputs and results, replies).
func writeClaudeTranscript(t testing.TB, path string, size int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	big := strings.Repeat("x", 6000)
	for i, written := 0, 0; written < size; i++ {
		var line string
		switch i % 4 {
		case 0:
			line = fmt.Sprintf(`{"type":"user","timestamp":"2026-10-15T10:00:00Z","message":{"role":"user","content":"please fix test number %d"}}`, i)
		case 1:
			line = fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-15T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"step %d"},{"type":"tool_use","id":"tu_%d","name":"Bash","input":{"command":"go test %s"}}]}}`, i, i, big[:800])
		case 2:
			line = fmt.Sprintf(`{"type":"user","timestamp":"2026-10-15T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_%d","content":"%s"}]}}`, i-1, big)
		default:
			line = fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-15T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"done %d %s"}]}}`, i, big[:300])
		}
		n, _ := f.WriteString(line + "\n")
		written += n
	}
}

// #3710: a resumed session's chat reads its ancestor's transcript too; the minute-by-minute cache
// prune kept only live session ids, so it dropped the ancestor and the next poll re-parsed it
// whole (4 to 14 s on a 100 MB transcript). The ancestor is kept now; an ended session's cache
// still goes.
func TestChatKeepsAResumedSessionsAncestorAcrossAPrune(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	server, h, _, ss := setupSessionsTestServer(t)
	wd := t.TempDir()
	const anc, cur, ended = "aaaaaaaa-3710-4000-8000-00000000000a", "bbbbbbbb-3710-4000-8000-00000000000b", "cccccccc-3710-4000-8000-00000000000c"
	dir := filepath.Join(projects, transcriptlink.ClaudeProjectDirName(wd))
	for _, id := range []string{anc, cur, ended} {
		writeClaudeTranscript(t, filepath.Join(dir, id+".jsonl"), 256<<10)
	}
	ancID := anc
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: cur, AgentName: "dev", AgentType: "claude", WorkingDir: wd, ResumeFromID: &ancID}))
	get := func(sid, q string) int {
		resp, err := http.Get(server.URL + "/api/sessions/live/dev/chat?session_id=" + sid + "&agent_type=claude&working_directory=" + wd + "&" + q)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out struct {
			Total int `json:"total"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out.Total
	}
	total := get(cur, "after=0&limit=100&offset=0")
	require.Positive(t, total)
	get(ended, "after=0&limit=100&offset=0") // a session that isn't live (any more)
	require.True(t, h.jsonl.IsCached(anc) && h.jsonl.IsCached(cur) && h.jsonl.IsCached(ended))

	h.pruneSessionCaches(context.Background())
	assert.True(t, h.jsonl.IsCached(cur), "the live session stays")
	assert.True(t, h.jsonl.IsCached(anc), "the ancestor it resumed from stays")
	assert.False(t, h.jsonl.IsCached(ended), "an ended session's transcript is dropped")
	assert.Equal(t, total, get(cur, "after="+strconv.Itoa(total)), "the poll answers from the cache")
}
