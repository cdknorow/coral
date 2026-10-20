package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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

	sid := uniqueSID(430)
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

// #4306 follow-up: through the real route, a delivery flags one turn. The operator typing the same long text before Coral did, or
// again after it, keeps their turns; a short text Coral delivered ("continue") never flags the operator's identical message; a
// delivered text with a slash command in front is not the same text; and the flag does not depend on the page asked for.
func TestSessionsChat_DeliveryFlagsOneTurnAndNeverAShortText(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	sid := uniqueSID(431)
	notice := "[Task #4300 completed by QA Engineer] Outcome: success. The same long text the operator may have typed."
	now := time.Now().UTC()
	line := func(at time.Time, text string) string {
		b, _ := json.Marshal(map[string]any{"type": "user", "timestamp": at.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": text}})
		return string(b) + "\n"
	}
	// 0: the operator types the long text an hour before Coral delivers it; 1: "continue" (also delivered, short); 2: the delivery
	// (pasted); 3: the operator repeats the text; 4: a slash command in front of the delivered text; 5: the operator's "continue".
	transcript := line(now.Add(-time.Hour), notice) + line(now.Add(-time.Minute), "continue") + line(now.Add(time.Second), "<pasted_content id=\"1\">\n"+notice+"\n</pasted_content id=\"1\">") +
		line(now.Add(2*time.Second), notice) + line(now.Add(3*time.Second), "/status "+notice) + line(now.Add(4*time.Second), "continue")
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sid+".jsonl"), []byte(transcript), 0644))

	deliver := func(text string) {
		t.Helper()
		d, err := ptymanager.DeliverAutomated(context.Background(), ptymanager.AutomatedInput{
			Name: "claude-" + sid, AgentType: "claude", SessionID: sid, Text: text, What: "test", Send: func(context.Context) error { return nil },
		})
		require.NoError(t, err)
		require.Equal(t, ptymanager.Sent, d)
	}
	deliver("continue")
	deliver(notice)

	get := func(query string) (flags []bool, start int) {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("%s/api/sessions/live/claude-%s/chat?session_id=%s&agent_type=claude&%s", server.URL, sid, sid, query))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Messages []map[string]any `json:"messages"`
			Start    int              `json:"start"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		for _, m := range body.Messages {
			v, _ := m["coral_notice"].(bool)
			flags = append(flags, v)
		}
		return flags, body.Start
	}
	want := []bool{false, false, true, false, false, false}
	got, _ := get("after=0&limit=30")
	assert.Equal(t, want, got, "only the delivered turn is flagged: not the operator's earlier or repeated copy, not 'continue', not the slash-prefixed text")
	got, start := get("after=0&limit=3&before=4")
	assert.Equal(t, 1, start)
	assert.Equal(t, want[1:4], got, "an older page is flagged the same way (the delivery's turn is found in the whole transcript)")
	got, _ = get("after=3")
	assert.Equal(t, want[3:], got, "and the poll for new turns")
}

var sidSeq atomic.Int64

// uniqueSID is a session id no other run of any test in this process has used. The automated-input gate remembers what it typed into a session in
// package state, so a test run again in the same process (go test -count=N) found the notice already delivered before it sent it (#5148).
func uniqueSID(base int) string {
	return fmt.Sprintf("00000000-0000-0000-%04d-%012d", sidSeq.Add(1), base)
}

// #5482: a turn the Operator sent from the dashboard comes back "coral_notice": false, so the page does not take their "[Coral ...]" or "Stop hook feedback:" for a Coral notice by its words; a turn Coral
// delivered is true; every other turn carries no flag. A text that is both (the delivery came first) is the delivery's.
func TestSessionsChat_OperatorTurnsAreFlaggedTheirs(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	sid := uniqueSID(520)
	notice := "[Task #4301 completed by QA Engineer] Outcome: success. Typed by Coral through the gate, once."
	line := func(v any) string { b, _ := json.Marshal(v); return string(b) + "\n" }
	user := func(text string) string {
		return line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
	}
	transcript := user("a plain question") + user("[Coral health check] am I still here?") + user(notice) + user("Stop hook feedback: I typed this myself") + user("[Task #1] mine too")
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sid+".jsonl"), []byte(transcript), 0644))
	get := func() []map[string]any {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("%s/api/sessions/live/claude-%s/chat?session_id=%s&agent_type=claude&after=0&limit=30", server.URL, sid, sid))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body.Messages
	}
	origin := func(msgs []map[string]any) []string { // "" no origin recorded, "operator" false, "coral" true
		var out []string
		for _, m := range msgs {
			switch v, ok := m["coral_notice"].(bool); {
			case !ok:
				out = append(out, "")
			case v:
				out = append(out, "coral")
			default:
				out = append(out, "operator")
			}
		}
		return out
	}
	assert.Equal(t, []string{"", "", "", "", ""}, origin(get()), "nothing remembered: no origin on any turn")

	name := "claude-" + sid
	ptymanager.NoteOperatorText(name, "[Coral health check] am I still here?")
	ptymanager.NoteOperatorText(name, "Stop hook feedback: I typed this myself")
	assert.Equal(t, []string{"", "operator", "", "operator", ""}, origin(get()), "what the Operator sent is theirs; the rest is still read by its words")

	d, err := ptymanager.DeliverAutomated(context.Background(), ptymanager.AutomatedInput{Name: name, AgentType: "claude", SessionID: sid, Text: notice, What: "test", Send: func(context.Context) error { return nil }})
	require.NoError(t, err)
	require.Equal(t, ptymanager.Sent, d)
	ptymanager.NoteOperatorText(name, notice) // the same text sent by a person too: the delivery came first and keeps its turn
	assert.Equal(t, []string{"", "operator", "coral", "operator", ""}, origin(get()))
}
