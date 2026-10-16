package routes

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// menuOnScreen is a terminal showing an open permission menu: the gate holds for it.
type menuOnScreen struct{}

func (menuOnScreen) AgentScreen(context.Context, string, string, string) (string, error) {
	return " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel · Tab to amend\n", nil
}

// #3481 phase 1: the gate's state from the agent's own signals: none yet (ok=false),
// in a turn, blocked on its own request (with the kind), idle after a stop, idle again
// after a denial that only the transcript records, and gone after SessionEnd, which
// also drops what Coral held for it.
func TestGateStateFromTheAgentsSignals(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "dddddddd-3481-1a00-0000-000000000004"
	name := "claude-" + sid
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "delta", WorkingDir: "/work"}))
	require.NoError(t, ss.SetCLIVersion(ctx, sid, "2.1.300"))
	tok := agentToken(t, h, sid, name)
	post := func(body string) {
		body = strings.Replace(body, "{", `{"hooks_rev":`+strconv.Itoa(agent.ClaudeHooksRev)+`,`, 1) // a current agent's hooks (#3655)
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/delta/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.SessionTokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
	}
	state := func() (ptymanager.AgentState, bool) { return h.gateState(ctx, name, "claude", sid) }
	// The session's transcript, where Claude records what no hook reports.
	dir := filepath.Join(projects, "-work")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	transcript := filepath.Join(dir, sid+".jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"Pick a colour"},"timestamp":"2026-01-01T00:00:00Z"}`+"\n"), 0o600))

	_, ok := state()
	assert.False(t, ok, "no signal yet: the gate keeps its screen rule")

	post(`{"event_type":"prompt_submit","summary":"User submitted prompt"}`)
	st, ok := state()
	require.True(t, ok)
	assert.Equal(t, ptymanager.StateInTurn, st.State)
	assert.Equal(t, "2.1.300", st.CLIVersion)

	post(`{"event_type":"permission_request","summary":"Waiting for the user: question (AskUserQuestion)","tool_name":"AskUserQuestion","detail_json":{"kind":"question","tool_name":"AskUserQuestion"}}`)
	post(`{"event_type":"tool_use","summary":"Read a.go","tool_name":"Read"}`)
	st, _ = state()
	assert.Equal(t, ptymanager.StateBlocked, st.State, "a parallel tool doesn't clear the question")
	assert.Equal(t, "question", st.Kind)

	// The user denies it: Claude fires no hook, its transcript records the turn's end.
	// (The merge needs the record a whole second newer than the hook events.)
	end := time.Now().UTC().Add(2 * time.Second).Format(time.RFC3339Nano)
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]},"timestamp":"` + end + `"}` + "\n" +
		`{"type":"system","subtype":"turn_duration","durationMs":900,"timestamp":"` + end + `"}` + "\n")
	require.NoError(t, err)
	f.Close()
	st, _ = state()
	assert.Equal(t, ptymanager.StateIdle, st.State, "the transcript's end of turn clears the denied request")
	assert.False(t, st.LastStop.IsZero())

	// SessionEnd: gone, and a send Coral held for the old process is dropped.
	_, err = ptymanager.DeliverAutomated(ctx, ptymanager.AutomatedInput{Term: menuOnScreen{}, Name: name, AgentType: "claude", SessionID: sid,
		Key: "test-3481", Text: "a nudge", What: "test", Send: func(context.Context) error { return nil }})
	require.NoError(t, err)
	require.Equal(t, 1, ptymanager.HeldCount(name))
	post(`{"event_type":"session_end","summary":"Session ended: other","detail_json":{"reason":"other"}}`)
	st, _ = state()
	assert.Equal(t, ptymanager.StateGone, st.State)
	assert.Zero(t, ptymanager.HeldCount(name), "held sends dropped when the session ended")
}
