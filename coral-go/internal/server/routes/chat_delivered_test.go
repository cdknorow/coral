package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/ptymanager"
)

// #4306: a turn that Coral typed into the agent comes back flagged coral_notice, however long or odd its text (a task notice
// of a form no text pattern knows, wrapped in pasted_content tags as Claude Code records a long paste); a turn the operator
// typed, even one that quotes the notice, is not flagged; and the transcript reader's shared messages are never edited.
func TestSessionsChat_FlagsTurnsCoralDelivered(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	sid := "00000000-0000-0000-0000-000000000430"
	notice := "[Task #4300 completed by QA Engineer] Outcome: success. A form no pattern knows: spoke of a thing."
	line := func(v any) string { b, _ := json.Marshal(v); return string(b) + "\n" }
	user := func(text string) string {
		return line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
	}
	transcript := user("please look at the batch") +
		user("<pasted_content id=\"1\">\n"+notice+"\n</pasted_content id=\"1\">") +
		user("what does this mean: "+notice) +
		user("[Task #9 completed by someone] typed by the operator himself")
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sid+".jsonl"), []byte(transcript), 0644))

	get := func(query string) []map[string]any {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("%s/api/sessions/live/claude-%s/chat?session_id=%s&agent_type=claude&%s", server.URL, sid, sid, query))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body.Messages
	}
	flagged := func(msgs []map[string]any) []bool {
		var out []bool
		for _, m := range msgs {
			v, _ := m["coral_notice"].(bool)
			out = append(out, v)
		}
		return out
	}

	assert.Equal(t, []bool{false, false, false, false}, flagged(get("after=0&limit=30")), "Coral has typed nothing into this session: nothing is flagged")

	// Coral types the notice through the automated-input gate.
	d, err := ptymanager.DeliverAutomated(context.Background(), ptymanager.AutomatedInput{
		Name: "claude-" + sid, AgentType: "claude", SessionID: sid, Text: notice, What: "test",
		Send: func(context.Context) error { return nil },
	})
	require.NoError(t, err)
	require.Equal(t, ptymanager.Sent, d)

	assert.Equal(t, []bool{false, true, false, false}, flagged(get("after=0&limit=30")), "only the turn Coral typed is flagged, the wrapped paste included")
	assert.Equal(t, []bool{false, true, false, false}, flagged(get("after=0&limit=30&trim=300")), "also on a trimmed page")
	assert.Equal(t, []bool{true, false, false}, flagged(get("after=1")), "and on the poll for new messages")
}
